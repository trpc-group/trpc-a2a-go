// Tencent is pleased to support the open source community by making trpc-a2a-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-a2a-go is licensed under the Apache License Version 2.0.

package protocol

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPartMarshalText(t *testing.T) {
	p := NewTextPart("hello world")
	data, err := json.Marshal(p)
	require.NoError(t, err)
	assert.JSONEq(t, `{"text":"hello world"}`, string(data))
}

func TestPartMarshalURL(t *testing.T) {
	p := NewFilePart("https://example.com/doc.pdf", "doc.pdf", "application/pdf")
	data, err := json.Marshal(p)
	require.NoError(t, err)
	assert.JSONEq(t, `{"url":"https://example.com/doc.pdf","filename":"doc.pdf","mediaType":"application/pdf"}`, string(data))
}

func TestPartMarshalRaw(t *testing.T) {
	p := NewRawPart([]byte{0xff, 0xfe}, "image/png")
	data, err := json.Marshal(p)
	require.NoError(t, err)
	assert.JSONEq(t, `{"raw":"//4=","mediaType":"image/png"}`, string(data))
}

func TestPartMarshalData(t *testing.T) {
	p := NewDataPart(map[string]any{"key": "value"})
	data, err := json.Marshal(p)
	require.NoError(t, err)
	assert.JSONEq(t, `{"data":{"key":"value"}}`, string(data))
}

func TestPartMarshalDataScalar(t *testing.T) {
	p := NewDataPart("just a string")
	data, err := json.Marshal(p)
	require.NoError(t, err)
	assert.JSONEq(t, `{"data":"just a string"}`, string(data))
}

func TestPartUnmarshalText(t *testing.T) {
	var p Part
	err := json.Unmarshal([]byte(`{"text":"hello"}`), &p)
	require.NoError(t, err)
	assert.Equal(t, Text("hello"), p.Content)
	assert.Equal(t, "hello", p.TextContent())
}

func TestPartUnmarshalURL(t *testing.T) {
	var p Part
	err := json.Unmarshal([]byte(`{"url":"https://example.com","mediaType":"image/png","filename":"img.png"}`), &p)
	require.NoError(t, err)
	assert.Equal(t, URL("https://example.com"), p.Content)
	assert.Equal(t, "image/png", p.MediaType)
	assert.Equal(t, "img.png", p.Filename)
}

func TestPartUnmarshalRaw(t *testing.T) {
	var p Part
	err := json.Unmarshal([]byte(`{"raw":"//4=","mediaType":"application/pdf"}`), &p)
	require.NoError(t, err)
	assert.Equal(t, Raw([]byte{0xff, 0xfe}), p.Content)
	assert.Equal(t, "application/pdf", p.MediaType)
}

func TestPartUnmarshalData(t *testing.T) {
	var p Part
	err := json.Unmarshal([]byte(`{"data":{"key":"value"}}`), &p)
	require.NoError(t, err)
	d, ok := p.Content.(Data)
	require.True(t, ok)
	m, ok := d.Value.(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "value", m["key"])
}

func TestPartUnmarshalMultipleContentFields(t *testing.T) {
	var p Part
	err := json.Unmarshal([]byte(`{"text":"hello","url":"https://example.com"}`), &p)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "exactly one content field")
}

func TestPartUnmarshalNoContentField(t *testing.T) {
	var p Part
	err := json.Unmarshal([]byte(`{"filename":"test.txt"}`), &p)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "exactly one content field")
}

func TestPartRoundTrip(t *testing.T) {
	cases := []struct {
		name string
		part *Part
	}{
		{"text", NewTextPart("hello")},
		{"url", NewFilePart("https://example.com/f.pdf", "f.pdf", "application/pdf")},
		{"raw", NewRawPart([]byte{1, 2, 3, 4}, "application/octet-stream")},
		{"data_map", NewDataPart(map[string]any{"a": float64(1)})},
		{"data_string", NewDataPart("scalar")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			data, err := json.Marshal(tc.part)
			require.NoError(t, err)

			var got Part
			err = json.Unmarshal(data, &got)
			require.NoError(t, err)
			assert.Equal(t, tc.part.Content, got.Content)
			assert.Equal(t, tc.part.Filename, got.Filename)
			assert.Equal(t, tc.part.MediaType, got.MediaType)
		})
	}
}

func TestPartWithMetadata(t *testing.T) {
	p := &Part{
		Content:  Text("hello"),
		Metadata: map[string]any{"source": "test"},
	}
	data, err := json.Marshal(p)
	require.NoError(t, err)
	assert.JSONEq(t, `{"text":"hello","metadata":{"source":"test"}}`, string(data))

	var got Part
	err = json.Unmarshal(data, &got)
	require.NoError(t, err)
	assert.Equal(t, "test", got.Metadata["source"])
}

func TestConvenienceAccessors(t *testing.T) {
	textPart := NewTextPart("hello")
	assert.Equal(t, "hello", textPart.TextContent())
	assert.Equal(t, "", textPart.URLContent())
	assert.Nil(t, textPart.RawContent())
	assert.Nil(t, textPart.DataContent())

	urlPart := NewURLPart("https://example.com", "text/html")
	assert.Equal(t, "", urlPart.TextContent())
	assert.Equal(t, "https://example.com", urlPart.URLContent())

	rawPart := NewRawPart([]byte{1, 2}, "application/octet-stream")
	assert.Equal(t, []byte{1, 2}, rawPart.RawContent())

	dataPart := NewDataPart(42.0)
	assert.Equal(t, 42.0, dataPart.DataContent())

	var nilPart *Part
	assert.Equal(t, "", nilPart.TextContent())
	assert.Nil(t, nilPart.RawContent())
}
