/* Tiny libc bits so clang does not emit env.memmove/memcpy imports. */
#include <stdint.h>

void *memcpy(void *dst, const void *src, unsigned long n)
{
	uint8_t *d = (uint8_t *)dst;
	const uint8_t *s = (const uint8_t *)src;
	while (n--)
		*d++ = *s++;
	return dst;
}

void *memmove(void *dst, const void *src, unsigned long n)
{
	uint8_t *d = (uint8_t *)dst;
	const uint8_t *s = (const uint8_t *)src;
	if (d == s || n == 0)
		return dst;
	if (d < s) {
		while (n--)
			*d++ = *s++;
	} else {
		d += n;
		s += n;
		while (n--)
			*--d = *--s;
	}
	return dst;
}

void *memset(void *dst, int c, unsigned long n)
{
	uint8_t *d = (uint8_t *)dst;
	uint8_t v = (uint8_t)c;
	while (n--)
		*d++ = v;
	return dst;
}
