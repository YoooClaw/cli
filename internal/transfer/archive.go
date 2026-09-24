package transfer

import (
	"archive/tar"
	"bufio"
	"compress/gzip"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// 迁移包的单文件形态：包目录（manifest.json + blobs/<sha256>）打成 gzip 压缩的 ustar。
// 与插件 src/transfer/archive.ts 产出同样的格式，两边互相可读。
//
// 解包只认白名单条目：普通文件 manifest.json、blobs/<sha256> 与 blobs/ 目录项；
// 链接、PAX/GNU 扩展头、路径穿越、重复条目一律拒绝。解出的内容仍须经 ReadPackage
// 全量校验后才能使用。

// MaxExpansionRatio 限制解压后体积 = 压缩包体积 × 该倍数 + manifest 上限，挡住压缩炸弹。
const MaxExpansionRatio = 100

// PackageFormats 是本端能读写的包形态。旧版本的能力文件没有该字段，视为只支持 directory。
var PackageFormats = []string{"directory", "tar.gz"}

var (
	blobNameRE = regexp.MustCompile(`^blobs/[a-f0-9]{64}$`)
	archiveRE  = regexp.MustCompile(`(?i)\.(tar\.gz|tgz)$`)
)

// maxArchiveEntries 与插件一致：每条记录最多 5 个附件字段，外加 manifest 与 blobs/ 目录项。
const maxArchiveEntries = MaxRecords*5 + 2

// IsArchivePath 判断 --out/--file 路径是否指向单文件包。
func IsArchivePath(path string) bool { return archiveRE.MatchString(path) }

// ExportArchive 导出单文件包：先按目录形态写到 output 旁的临时目录（同一文件系统，
// 同样受 OUTPUT_INSIDE_SOURCE 约束），再打包并删除临时目录。返回压缩包字节数。
func ExportArchive(roots Roots, output string, opts ExportOptions) (*Manifest, int64, error) {
	if _, err := os.Lstat(output); err == nil {
		return nil, 0, fail("OUTPUT_EXISTS")
	}
	staging := filepath.Join(filepath.Dir(output), "."+filepath.Base(output)+"."+newUUID()+".partial-dir")
	defer os.RemoveAll(staging)
	m, err := Export(roots, staging, opts)
	if err != nil {
		return nil, 0, err
	}
	size, err := PackDirectory(staging, output)
	if err != nil {
		return nil, 0, err
	}
	return m, size, nil
}

// PackDirectory 把已写好的包目录打成 tar.gz。先写同目录临时文件再 rename，
// 中途失败不会留下半个包。
func PackDirectory(dir, output string) (int64, error) {
	list, err := os.ReadDir(filepath.Join(dir, "blobs"))
	if err != nil {
		return 0, err
	}
	var blobs []string
	for _, e := range list {
		if blobNameRE.MatchString("blobs/" + e.Name()) {
			blobs = append(blobs, e.Name())
		}
	}
	sort.Strings(blobs)
	temp := filepath.Join(filepath.Dir(output), "."+filepath.Base(output)+"."+newUUID()+".partial")
	defer os.Remove(temp)
	f, err := os.OpenFile(temp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return 0, err
	}
	werr := func() error {
		buf := bufio.NewWriterSize(f, 1<<20)
		gz := gzip.NewWriter(buf)
		tw := tar.NewWriter(gz)
		if err := addFile(tw, "manifest.json", filepath.Join(dir, "manifest.json")); err != nil {
			return err
		}
		if err := tw.WriteHeader(&tar.Header{Name: "blobs/", Typeflag: tar.TypeDir, Mode: 0o700, ModTime: time.Unix(0, 0), Format: tar.FormatUSTAR}); err != nil {
			return err
		}
		for _, b := range blobs {
			if err := addFile(tw, "blobs/"+b, filepath.Join(dir, "blobs", b)); err != nil {
				return err
			}
		}
		if err := tw.Close(); err != nil {
			return err
		}
		if err := gz.Close(); err != nil {
			return err
		}
		return buf.Flush()
	}()
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		return 0, werr
	}
	if _, err := os.Lstat(output); err == nil {
		return 0, fail("OUTPUT_EXISTS")
	}
	if err := os.Rename(temp, output); err != nil {
		return 0, err
	}
	info, err := os.Stat(output)
	if err != nil {
		return 0, err
	}
	return info.Size(), nil
}

func addFile(tw *tar.Writer, name, path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return err
	}
	if err := tw.WriteHeader(&tar.Header{Name: name, Typeflag: tar.TypeReg, Mode: 0o600, Size: info.Size(), ModTime: time.Unix(0, 0), Format: tar.FormatUSTAR}); err != nil {
		return err
	}
	if n, err := io.Copy(tw, f); err != nil || n != info.Size() {
		return fail("SOURCE_CHANGED")
	}
	return nil
}

// ExtractArchive 严格解包到一个已存在、含空 blobs/ 的 staging 目录。失败时由调用方清理目录。
func ExtractArchive(file, dest string) error {
	info, err := os.Stat(file)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fail("ARCHIVE_INVALID")
	}
	f, err := os.Open(file)
	if err != nil {
		return err
	}
	defer f.Close()
	gz, err := gzip.NewReader(bufio.NewReaderSize(f, 1<<20))
	if err != nil {
		return fail("ARCHIVE_INVALID")
	}
	defer gz.Close()
	limit := info.Size()*MaxExpansionRatio + MaxManifestBytes
	tr := tar.NewReader(gz)
	seen := map[string]bool{}
	var total int64
	for count := 1; ; count++ {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return corrupt(err)
		}
		if count > maxArchiveEntries {
			return fail("ARCHIVE_TOO_LARGE")
		}
		// 标准库会把 PAX/GNU 扩展头合并进 hdr；出现过就说明不是迁移导出产的包。
		// 普通 ustar 头读出来 Format 是 unknown（与 PAX 无法区分），不能按它放行。
		if hdr.Format&tar.FormatGNU != 0 || len(hdr.PAXRecords) > 0 {
			return fail("ARCHIVE_UNSAFE_ENTRY")
		}
		name := strings.TrimPrefix(hdr.Name, "./")
		if hdr.Typeflag == tar.TypeDir {
			if (name != "blobs/" && name != "blobs") || hdr.Size != 0 {
				return fail("ARCHIVE_UNSAFE_ENTRY")
			}
			continue
		}
		if (hdr.Typeflag != tar.TypeReg && hdr.Typeflag != '\x00') || (name != "manifest.json" && !blobNameRE.MatchString(name)) {
			return fail("ARCHIVE_UNSAFE_ENTRY")
		}
		if seen[name] {
			return fail("ARCHIVE_DUPLICATE_ENTRY")
		}
		seen[name] = true
		if name == "manifest.json" && hdr.Size > MaxManifestBytes {
			return fail("MANIFEST_TOO_LARGE")
		}
		if total += hdr.Size; total > limit {
			return fail("ARCHIVE_TOO_LARGE")
		}
		if err := writeEntry(tr, filepath.Join(dest, filepath.FromSlash(name)), hdr.Size); err != nil {
			return err
		}
	}
	if !seen["manifest.json"] {
		return fail("ARCHIVE_INVALID")
	}
	return nil
}

func writeEntry(r io.Reader, path string, size int64) error {
	out, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	_, cerr := io.CopyN(out, r, size)
	if err := out.Close(); cerr == nil {
		cerr = err
	}
	return corrupt(cerr)
}

// corrupt 把解压/解包格式错误（截断、坏 CRC、坏头）归为 ARCHIVE_INVALID；
// 磁盘写满等系统错误原样返回。
func corrupt(err error) error {
	var pathErr *os.PathError
	if err == nil || errors.As(err, &pathErr) {
		return err
	}
	return fail("ARCHIVE_INVALID")
}

// CheckArchiveSupport 在导出 tar.gz 前确认目标端能读；旧目标端只能收目录包。
func CheckArchiveSupport(c map[string]any) error {
	formats, _ := c["packageFormats"].([]any)
	for _, f := range formats {
		if f == "tar.gz" {
			return nil
		}
	}
	return fail("ARCHIVE_UNSUPPORTED_BY_TARGET")
}
