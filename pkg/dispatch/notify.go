package dispatch

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	ActionNotify = "notify"
	ActionClip   = "clip"
	MaxClipBytes = 512 << 10 // 512 KiB

	maxTitleRunes   = 80
	maxMessageRunes = 400

	// maxExecBytes keeps an encoded Exec well under tether's 1 MiB frame limit,
	// which closes the connection on anything larger. JSON escaping can grow
	// clip text several times over.
	maxExecBytes = 1<<20 - 16<<10
)

type NotifyArgs struct {
	Title    string `json:"title"`              // required, <= 80 runes
	Subtitle string `json:"subtitle,omitempty"` // <= 80 runes
	Message  string `json:"message"`            // required, <= 400 runes
	URL      string `json:"url,omitempty"`      // optional click target; same rules as open_url
}

type ClipArgs struct {
	Text string `json:"text"` // valid UTF-8, 1..MaxClipBytes bytes
}

// NewNotify normalizes and validates a, and builds a notify Exec with a new ID.
func NewNotify(a NotifyArgs) (Exec, error) {
	a, err := a.normalize()
	if err != nil {
		return Exec{}, err
	}
	return newExec(ActionNotify, a)
}

// ParseNotify decodes the args of a notify Exec, returning them normalized.
func ParseNotify(e Exec) (NotifyArgs, error) {
	var a NotifyArgs
	if err := parseExec(e, ActionNotify, &a); err != nil {
		return NotifyArgs{}, err
	}
	return a.normalize()
}

// NewClip validates text and builds a clip Exec with a new ID.
func NewClip(text string) (Exec, error) {
	a := ClipArgs{Text: text}
	if err := a.validate(); err != nil {
		return Exec{}, err
	}
	e, err := newExec(ActionClip, a)
	if err != nil {
		return Exec{}, err
	}
	if data, err := json.Marshal(e); err != nil || len(data) > maxExecBytes {
		return Exec{}, errors.New("dispatch: clip text too large to send once escaped")
	}
	return e, nil
}

// ParseClip decodes and validates the args of a clip Exec.
func ParseClip(e Exec) (ClipArgs, error) {
	var a ClipArgs
	if err := parseExec(e, ActionClip, &a); err != nil {
		return ClipArgs{}, err
	}
	if err := a.validate(); err != nil {
		return ClipArgs{}, err
	}
	return a, nil
}

func (a ClipArgs) validate() error {
	switch {
	case a.Text == "":
		return errors.New("dispatch: empty clip text")
	case len(a.Text) > MaxClipBytes:
		return fmt.Errorf("dispatch: clip text longer than %d bytes", MaxClipBytes)
	case !utf8.ValidString(a.Text):
		return errors.New("dispatch: clip text is not valid UTF-8")
	}
	return nil
}

func (a NotifyArgs) normalize() (NotifyArgs, error) {
	a.Title = clean(a.Title, maxTitleRunes, false)
	a.Subtitle = clean(a.Subtitle, maxTitleRunes, false)
	a.Message = clean(a.Message, maxMessageRunes, true)
	switch {
	case a.Title == "":
		return NotifyArgs{}, errors.New("dispatch: notify needs a title")
	case a.Message == "":
		return NotifyArgs{}, errors.New("dispatch: notify needs a message")
	}
	if a.URL != "" {
		if err := (OpenURLArgs{URL: a.URL}).validate(); err != nil {
			return NotifyArgs{}, err
		}
	}
	return a, nil
}

// clean strips control characters (a newline becomes a space if flatten is
// set), trims, and cuts to limit runes with a trailing ellipsis.
func clean(s string, limit int, flatten bool) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r == '\n' && flatten:
			b.WriteByte(' ')
		case unicode.IsControl(r):
		default:
			b.WriteRune(r)
		}
	}
	out := strings.TrimSpace(b.String())
	if utf8.RuneCountInString(out) > limit {
		out = string([]rune(out)[:limit-1]) + "…"
	}
	return out
}
