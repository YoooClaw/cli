package autostart

import (
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
)

const deferredTriggerID = "YoooClawAgentDeferredStart"

var errTaskDefinitionMismatch = errors.New("计划任务定义不匹配")

type taskDefinition struct {
	Principals []struct {
		UserID    string `xml:"UserId"`
		LogonType string
		RunLevel  string
	} `xml:"Principals>Principal"`
	Actions struct {
		Exec  []struct{ Command, Arguments, WorkingDirectory string }
		Inner string `xml:",innerxml"`
	}
	Settings struct{ Enabled bool }
	Triggers struct {
		Logon []struct {
			UserID  string `xml:"UserId"`
			Enabled bool
		} `xml:"LogonTrigger"`
		Time []struct {
			ID            string `xml:"id,attr"`
			StartBoundary string
			Enabled       bool
		} `xml:"TimeTrigger"`
	}
}

func taskDecoder(raw string) *xml.Decoder {
	d := xml.NewDecoder(strings.NewReader(raw))
	// Task Scheduler returns BSTR converted to UTF-8 by go-ole, while its XML
	// declaration still says UTF-16. No byte-level conversion is needed here.
	d.CharsetReader = func(charset string, r io.Reader) (io.Reader, error) {
		if strings.EqualFold(charset, "UTF-16") {
			return r, nil
		}
		return nil, fmt.Errorf("unexpected task XML charset %q", charset)
	}
	return d
}

func parseTaskDefinition(raw string) (taskDefinition, error) {
	var d taskDefinition
	err := taskDecoder(raw).Decode(&d)
	return d, err
}

// Rewrite only our named time trigger, preserving every other byte of the
// task, including logon triggers, settings, principal and the executable action.
func withDeferredTrigger(raw string, at time.Time) (string, error) {
	d := taskDecoder(raw)
	depth, triggersDepth, end := 0, 0, -1
	type span struct{ from, to int }
	var remove []span
	for {
		before := int(d.InputOffset())
		token, err := d.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", err
		}
		switch t := token.(type) {
		case xml.StartElement:
			depth++
			if t.Name.Local == "Triggers" && depth == 2 {
				triggersDepth = depth
			}
			if t.Name.Local == "TimeTrigger" && triggersDepth != 0 && depth == triggersDepth+1 {
				for _, a := range t.Attr {
					if a.Name.Local == "id" && a.Value == deferredTriggerID {
						if err := d.Skip(); err != nil {
							return "", err
						}
						remove = append(remove, span{before, int(d.InputOffset())})
						depth--
						break
					}
				}
			}
		case xml.EndElement:
			if t.Name.Local == "Triggers" && depth == triggersDepth {
				end = before
				triggersDepth = 0
			}
			depth--
		}
	}
	if end < 0 {
		return "", fmt.Errorf("existing task has no Triggers element")
	}
	insert := `<TimeTrigger id="` + deferredTriggerID + `"><StartBoundary>` + at.Format(time.RFC3339) + `</StartBoundary><Enabled>true</Enabled></TimeTrigger>`
	var b strings.Builder
	start := 0
	for _, s := range remove {
		b.WriteString(raw[start:s.from])
		start = s.to
	}
	b.WriteString(raw[start:end])
	b.WriteString(insert)
	b.WriteString(raw[end:])
	return b.String(), nil
}

// NativeInspector is optional so Unix service managers retain their existing
// behavior. "verified" is deliberately distinct from a task being Running.
type NativeInspector interface {
	Inspect(Spec, int) (Inspection, error)
	Schedule(Spec, time.Duration) (time.Time, error)
}

type Inspection struct {
	DefinitionMatches     bool   `json:"definitionMatches"`
	ManagedDaemonVerified bool   `json:"managedDaemonVerified"`
	DaemonPID             int    `json:"daemonPid,omitempty"`
	Reason                string `json:"reason,omitempty"`
}
