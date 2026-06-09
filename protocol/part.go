// Tencent is pleased to support the open source community by making trpc-a2a-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-a2a-go is licensed under the Apache License Version 2.0.

package protocol

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
)

// PartContent is a sealed interface for part content variants.
// Only the four types defined in this package (Text, Raw, URL, Data) implement it.
type PartContent interface {
	isPartContent()
}

// Text represents text content in a Part.
type Text string

// Raw represents binary content in a Part, encoded as base64 in JSON.
type Raw []byte

// URL represents a file URL reference in a Part.
type URL string

// Data represents structured data in a Part. Value can be any JSON-serializable value.
type Data struct {
	Value any
}

func (Text) isPartContent() {}
func (Raw) isPartContent()  {}
func (URL) isPartContent()  {}
func (Data) isPartContent() {}

// Part represents a content unit within a Message or Artifact.
// Content type is determined by which PartContent variant is set.
type Part struct {
	Content   PartContent    `json:"-"`
	Filename  string         `json:"-"`
	MediaType string         `json:"-"`
	Metadata  map[string]any `json:"-"`
}

// ContentParts is a slice of Part pointers.
type ContentParts []*Part

// TextContent returns the text content if this part is a Text part, otherwise "".
func (p *Part) TextContent() string {
	if p == nil {
		return ""
	}
	if t, ok := p.Content.(Text); ok {
		return string(t)
	}
	return ""
}

// RawContent returns the raw bytes if this part is a Raw part, otherwise nil.
func (p *Part) RawContent() []byte {
	if p == nil {
		return nil
	}
	if r, ok := p.Content.(Raw); ok {
		return []byte(r)
	}
	return nil
}

// URLContent returns the URL string if this part is a URL part, otherwise "".
func (p *Part) URLContent() string {
	if p == nil {
		return ""
	}
	if u, ok := p.Content.(URL); ok {
		return string(u)
	}
	return ""
}

// DataContent returns the data value if this part is a Data part, otherwise nil.
func (p *Part) DataContent() any {
	if p == nil {
		return nil
	}
	if d, ok := p.Content.(Data); ok {
		return d.Value
	}
	return nil
}

// NewTextPart creates a Part with text content.
func NewTextPart(text string) *Part {
	return &Part{Content: Text(text)}
}

// NewRawPart creates a Part with raw binary content.
func NewRawPart(raw []byte, mediaType string) *Part {
	return &Part{Content: Raw(raw), MediaType: mediaType}
}

// NewURLPart creates a Part with a URL reference.
func NewURLPart(url string, mediaType string) *Part {
	return &Part{Content: URL(url), MediaType: mediaType}
}

// NewFilePart creates a Part with a URL reference and filename.
func NewFilePart(url, filename, mediaType string) *Part {
	return &Part{Content: URL(url), Filename: filename, MediaType: mediaType}
}

// NewDataPart creates a Part with structured data.
func NewDataPart(data any) *Part {
	return &Part{Content: Data{Value: data}}
}

// partWire is the private mirror struct for Part JSON serialization.
// Fields map directly to the v1.0 JSON wire format.
type partWire struct {
	Text      *string        `json:"text,omitempty"`
	Raw       *string        `json:"raw,omitempty"`
	URL       *string        `json:"url,omitempty"`
	Data      *any           `json:"data,omitempty"`
	Filename  string         `json:"filename,omitempty"`
	MediaType string         `json:"mediaType,omitempty"`
	Metadata  map[string]any `json:"metadata,omitempty"`
}

// MarshalJSON implements the json.Marshaler interface.
func (p Part) MarshalJSON() ([]byte, error) {
	w := &partWire{
		Filename:  p.Filename,
		MediaType: p.MediaType,
		Metadata:  p.Metadata,
	}
	switch v := p.Content.(type) {
	case Text:
		s := string(v)
		w.Text = &s
	case Raw:
		s := base64.StdEncoding.EncodeToString([]byte(v))
		w.Raw = &s
	case URL:
		s := string(v)
		w.URL = &s
	case Data:
		w.Data = &v.Value
	case nil:
		return nil, errors.New("part content is nil")
	default:
		return nil, fmt.Errorf("unknown part content type: %T", v)
	}
	return json.Marshal(w)
}

// UnmarshalJSON implements the json.Unmarshaler interface.
func (p *Part) UnmarshalJSON(data []byte) error {
	var w partWire
	if err := json.Unmarshal(data, &w); err != nil {
		return err
	}
	p.Filename = w.Filename
	p.MediaType = w.MediaType
	p.Metadata = w.Metadata

	count := 0
	if w.Text != nil {
		count++
	}
	if w.Raw != nil {
		count++
	}
	if w.URL != nil {
		count++
	}
	if w.Data != nil {
		count++
	}
	if count != 1 {
		return fmt.Errorf("part must have exactly one content field, got %d", count)
	}

	switch {
	case w.Text != nil:
		p.Content = Text(*w.Text)
	case w.Raw != nil:
		decoded, err := base64.StdEncoding.DecodeString(*w.Raw)
		if err != nil {
			return fmt.Errorf("failed to decode base64 raw content: %w", err)
		}
		p.Content = Raw(decoded)
	case w.URL != nil:
		p.Content = URL(*w.URL)
	case w.Data != nil:
		p.Content = Data{Value: *w.Data}
	}
	return nil
}
