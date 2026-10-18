package forms

import (
	"fmt"
	"net/url"
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

// How a field's answers are checked. One table, by type, drives both
// submit's checks and the rules a definition publishes, so a site that
// checks in the browser checks what the server will.
//
// Patterns are written so Go's regexp and JavaScript's RegExp (with no
// flag, u or v, so an <input pattern> too) read them alike: no \s, \d or \w
// (their classes differ), the syntax characters escaped inside a class,
// anchored at both ends. They apply to the answer with its surrounding
// space trimmed.

const (
	emailPattern = `^[^\x00-\x20@<>,;]+@[^\x00-\x20@<>,;]+\.[^\x00-\x20@<>,;]+$`
	// Digits and + - ( ) . and spaces, with at least five digits.
	telPattern = `^[ +\(\)\.\-]*(?:[0-9][ +\(\)\.\-]*){5,}$`
	// http(s), a host, then anything printable. The server also parses it.
	urlPattern  = `^[hH][tT][tT][pP][sS]?:\/\/[^\x00-\x20\x7f\/?#@\\]+(?:[\/?#][^\x00-\x20\x7f]*)?$`
	datePattern = `^[0-9]{4}-[0-9]{2}-[0-9]{2}$`
	// A decimal number, as an <input type=number> sends one.
	numberPattern = `^-?(?:[0-9]+(?:\.[0-9]*)?|\.[0-9]+)(?:[eE][+\-]?[0-9]+)?$`
)

var emailFormat = regexp.MustCompile(emailPattern)

// A typeRule is what a field type's answers must be.
type typeRule struct {
	pattern string
	re      *regexp.Regexp
	// mismatch is the error when an answer fails the pattern or strict.
	mismatch string
	// strict is the server's own check beyond the pattern, for what a
	// regexp can't say (a real calendar date, a URL that parses).
	strict func(v string) bool
	// blank is the error when a required one is left empty.
	blank string
	// maxLength is the most characters an answer may have unless the field
	// sets its own; 0: not checked (files).
	maxLength int
	options   bool // answers must be among the field's options
	multiple  bool // more than one answer (checkboxes)
}

func rule(pattern, mismatch string, strict func(string) bool) typeRule {
	return typeRule{pattern: pattern, re: regexp.MustCompile(pattern), mismatch: mismatch, strict: strict, blank: "can't be blank", maxLength: 500}
}

var typeRules = map[string]typeRule{
	"text":     {blank: "can't be blank", maxLength: 500},
	"textarea": {blank: "can't be blank", maxLength: 10000},
	"hidden":   {blank: "can't be blank", maxLength: 500},
	"email":    rule(emailPattern, "isn't an email address", nil),
	"tel":      rule(telPattern, "isn't a phone number", nil),
	"url": rule(urlPattern, "isn't a web address (https://…)", func(v string) bool {
		u, err := url.Parse(v)
		return err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != ""
	}),
	"date": rule(datePattern, "isn't a date", func(v string) bool {
		_, err := time.Parse("2006-01-02", v)
		return err == nil
	}),
	"number": rule(numberPattern, "isn't a number", func(v string) bool {
		_, err := strconv.ParseFloat(v, 64)
		return err == nil
	}),
	"select":     {blank: "choose one", maxLength: 500, options: true},
	"radio":      {blank: "choose one", maxLength: 500, options: true},
	"checkboxes": {blank: "choose one", maxLength: 500, options: true, multiple: true},
	"checkbox":   {blank: "must be ticked", maxLength: 500},
	"file":       {blank: "choose a file"},
}

// defaultMaxMB is a file field's size limit unless it sets its own.
const defaultMaxMB = 5

// Rules are a field's checks, resolved: what a definition publishes and
// what submit enforces. A key that doesn't apply to the type is left out.
type Rules struct {
	Required  bool     `json:"required"`
	MaxLength int      `json:"max_length,omitempty"`
	Min       *float64 `json:"min,omitempty"`
	Max       *float64 `json:"max,omitempty"`
	Pattern   string   `json:"pattern,omitempty"`
	Options   []string `json:"options,omitempty"`
	Accept    []string `json:"accept,omitempty"`
	MaxMB     float64  `json:"max_mb,omitempty"`
}

// rulesFor resolves fl's rules from its type's and its own settings; nil
// for a layout field, which takes no answer.
func rulesFor(fl Field) *Rules {
	t, ok := typeRules[fl.Type]
	if !ok || fl.layout() {
		return nil
	}
	r := &Rules{Required: fl.Required, Pattern: t.pattern}
	if t.maxLength > 0 {
		r.MaxLength = t.maxLength
		if fl.MaxLength > 0 {
			r.MaxLength = fl.MaxLength
		}
	}
	if fl.Type == "number" {
		r.Min, r.Max = fl.Min, fl.Max
	}
	if t.options {
		r.Options = nonEmpty(fl.Options)
	}
	if fl.Type == "file" {
		r.MaxMB = fl.MaxMB
		if r.MaxMB <= 0 {
			r.MaxMB = defaultMaxMB
		}
		r.Accept = []string{}
		for a := range strings.SplitSeq(fl.Accept, ",") {
			if a = strings.ToLower(strings.TrimSpace(a)); a != "" {
				r.Accept = append(r.Accept, a)
			}
		}
	}
	return r
}

// blankMessage is the error for a required field left empty.
func blankMessage(fl Field) string {
	if t, ok := typeRules[fl.Type]; ok && t.blank != "" {
		return t.blank
	}
	return "can't be blank"
}

// checkValue is the first problem with a field's (non-empty) answers, ""
// when there's none.
func checkValue(fl Field, vals []string) string {
	r, t := rulesFor(fl), typeRules[fl.Type]
	if r == nil {
		return ""
	}
	v := vals[0]
	if r.MaxLength > 0 {
		for _, x := range vals {
			if len([]rune(x)) > r.MaxLength {
				return fmt.Sprintf("is longer than %d characters", r.MaxLength)
			}
		}
	}
	if t.re != nil && (!t.re.MatchString(v) || (t.strict != nil && !t.strict(v))) {
		return t.mismatch
	}
	if fl.Type == "number" {
		n, _ := strconv.ParseFloat(v, 64) // the pattern and strict passed
		if r.Min != nil && n < *r.Min {
			return fmt.Sprintf("is at least %g", *r.Min)
		}
		if r.Max != nil && n > *r.Max {
			return fmt.Sprintf("is at most %g", *r.Max)
		}
	}
	if t.options {
		if !t.multiple {
			if !slices.Contains(r.Options, v) {
				return "isn't one of the choices"
			}
			return ""
		}
		for _, x := range vals {
			if !slices.Contains(r.Options, x) {
				return x + " isn't one of the choices"
			}
		}
	}
	return ""
}

// checkFile is the problem with an uploaded file, "" when there's none. A
// type in accept is an extension (.pdf), a MIME type (application/pdf) or
// a family (image/*).
func checkFile(fl Field, pf postedFile) string {
	r := rulesFor(fl)
	if float64(pf.size) > r.MaxMB*(1<<20) {
		return fmt.Sprintf("is larger than %g MB", r.MaxMB)
	}
	if len(r.Accept) == 0 {
		return ""
	}
	ext := strings.ToLower(path.Ext(pf.Filename))
	for _, a := range r.Accept {
		if a == ext || a == pf.ContentType || (strings.HasSuffix(a, "/*") && strings.HasPrefix(pf.ContentType, strings.TrimSuffix(a, "*"))) {
			return ""
		}
	}
	return "isn't an allowed type (" + strings.Join(r.Accept, ", ") + ")"
}
