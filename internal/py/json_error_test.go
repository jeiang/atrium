package py

import (
	"errors"
	"testing"
)

var jsonErrorCorpus = []struct{ doc, want string }{
	{"", "Expecting value: line 1 column 1 (char 0)"},
	{"   ", "Expecting value: line 1 column 4 (char 3)"},
	{"\ufeff1", "Unexpected UTF-8 BOM (decode using utf-8-sig): line 1 column 1 (char 0)"},
	{"\ufeff", "Unexpected UTF-8 BOM (decode using utf-8-sig): line 1 column 1 (char 0)"},
	{"-", "Expecting value: line 1 column 1 (char 0)"},
	{"-x", "Expecting value: line 1 column 1 (char 0)"},
	{"-Infinit", "Expecting value: line 1 column 1 (char 0)"},
	{"nul", "Expecting value: line 1 column 1 (char 0)"},
	{"nulll", "Extra data: line 1 column 5 (char 4)"},
	{"tru", "Expecting value: line 1 column 1 (char 0)"},
	{"1 2", "Extra data: line 1 column 3 (char 2)"},
	{"[1,]", "Illegal trailing comma before end of array: line 1 column 3 (char 2)"},
	{"{\"a\":1,}", "Illegal trailing comma before end of object: line 1 column 7 (char 6)"},
	{"{", "Expecting property name enclosed in double quotes: line 1 column 2 (char 1)"},
	{"[", "Expecting value: line 1 column 2 (char 1)"},
	{"{\"a\"", "Expecting ':' delimiter: line 1 column 5 (char 4)"},
	{"{\"a\":", "Expecting value: line 1 column 6 (char 5)"},
	{"{\"a\":1", "Expecting ',' delimiter: line 1 column 7 (char 6)"},
	{"{\"a\" 1}", "Expecting ':' delimiter: line 1 column 6 (char 5)"},
	{"{1:2}", "Expecting property name enclosed in double quotes: line 1 column 2 (char 1)"},
	{"{\"a\":1 \"b\":2}", "Expecting ',' delimiter: line 1 column 8 (char 7)"},
	{"[1 2]", "Expecting ',' delimiter: line 1 column 4 (char 3)"},
	{"[1,", "Expecting value: line 1 column 4 (char 3)"},
	{"[,1]", "Expecting value: line 1 column 2 (char 1)"},
	{"{,}", "Expecting property name enclosed in double quotes: line 1 column 2 (char 1)"},
	{"\"abc", "Unterminated string starting at: line 1 column 1 (char 0)"},
	{"\"a\u000ab\"", "Invalid control character at: line 1 column 3 (char 2)"},
	{"\"a\\x\"", "Invalid \\escape: line 1 column 3 (char 2)"},
	{"\"\\", "Unterminated string starting at: line 1 column 1 (char 0)"},
	{"\"\\u12\"", "Invalid \\uXXXX escape: line 1 column 3 (char 2)"},
	{"\"\\u12zz\"", "Invalid \\uXXXX escape: line 1 column 3 (char 2)"},
	{"\"\\u1234", "Unterminated string starting at: line 1 column 1 (char 0)"},
	{"\"\\ud83d\\u12\"", "Invalid \\uXXXX escape: line 1 column 9 (char 8)"},
	{"\"\\ud83d\\udz00\"", "Invalid \\uXXXX escape: line 1 column 9 (char 8)"},
	{"\"\\ud83d\\ude00", "Unterminated string starting at: line 1 column 1 (char 0)"},
	{"01", "Extra data: line 1 column 2 (char 1)"},
	{"1.", "Extra data: line 1 column 2 (char 1)"},
	{"1e", "Extra data: line 1 column 2 (char 1)"},
	{"1.e5", "Extra data: line 1 column 2 (char 1)"},
	{"-01", "Extra data: line 1 column 3 (char 2)"},
	{"[1,\u000a2,\u000a x]", "Expecting value: line 3 column 2 (char 8)"},
	{"\u00e9 \u00e9 x", "Expecting value: line 1 column 1 (char 0)"},
	{"\"\u00e9\\q\"", "Invalid \\escape: line 1 column 3 (char 2)"},
	{"\u00e9", "Expecting value: line 1 column 1 (char 0)"},
	{"[\"\u00e9\", 1 2]", "Expecting ',' delimiter: line 1 column 9 (char 8)"},
	{"\u000a\u000a  \u000a{\"a\":\u000a  ?}", "Expecting value: line 5 column 3 (char 13)"},
	{"{\"\u00e9\":1,\u000a\"b\" 2}", "Expecting ':' delimiter: line 2 column 5 (char 12)"},
	{"[1,2]\u000a x", "Extra data: line 2 column 2 (char 7)"},
	{"1 ,", "Extra data: line 1 column 3 (char 2)"},
	{"{\"a\":1}}", "Extra data: line 1 column 8 (char 7)"},
	{"\"abc\"x", "Extra data: line 1 column 6 (char 5)"},
	{"[1]]", "Extra data: line 1 column 4 (char 3)"},
	{"tRue", "Expecting value: line 1 column 1 (char 0)"},
	{"[nan]", "Expecting value: line 1 column 2 (char 1)"},
	{"[-]", "Expecting value: line 1 column 2 (char 1)"},
	{"[-Infinity", "Expecting ',' delimiter: line 1 column 11 (char 10)"},
	{"{\"a\":NaN,\"b\":}", "Expecting value: line 1 column 14 (char 13)"},
	{"\u0009\u000d\u000a", "Expecting value: line 2 column 1 (char 3)"},
	{"\"\\u+123\"", "Invalid \\uXXXX escape: line 1 column 3 (char 2)"},
	{"\"\\u 123\"", "Invalid \\uXXXX escape: line 1 column 3 (char 2)"},
	{"\"\u0009\"", "Invalid control character at: line 1 column 2 (char 1)"},
	{"\U0001f600 x", "Expecting value: line 1 column 1 (char 0)"},
	{"\"\U0001f600\\y\"", "Invalid \\escape: line 1 column 3 (char 2)"},
	{"[\U0001f600,", "Expecting value: line 1 column 2 (char 1)"},
	{"{\"\U0001f600\" 1}", "Expecting ':' delimiter: line 1 column 6 (char 5)"},
	{"[1,2,\u000a3,\u000a4 5]", "Expecting ',' delimiter: line 3 column 3 (char 11)"},
	{"{\"a\":[1,2,}]", "Expecting value: line 1 column 11 (char 10)"},
	{"[{\"a\":1},]", "Illegal trailing comma before end of array: line 1 column 9 (char 8)"},
	{"1e5x", "Extra data: line 1 column 4 (char 3)"},
	{"[1e]", "Expecting ',' delimiter: line 1 column 3 (char 2)"},
	{"1.5.5", "Extra data: line 1 column 4 (char 3)"},
	{"--1", "Expecting value: line 1 column 1 (char 0)"},
	{"+1", "Expecting value: line 1 column 1 (char 0)"},
	{".5", "Expecting value: line 1 column 1 (char 0)"},
	{"0x10", "Extra data: line 1 column 2 (char 1)"},
	{"[1,2", "Expecting ',' delimiter: line 1 column 5 (char 4)"},
	{"[[1,2]", "Expecting ',' delimiter: line 1 column 7 (char 6)"},
	{"{\"a\":{\"b\":", "Expecting value: line 1 column 11 (char 10)"},
	{"{\"a\":\"b\",\u000a\"c\"}", "Expecting ':' delimiter: line 2 column 4 (char 13)"},
	{"{\u000a}\u000a{", "Extra data: line 3 column 1 (char 4)"},
	{"null x", "Extra data: line 1 column 6 (char 5)"},
	{"true false", "Extra data: line 1 column 6 (char 5)"},
	{"\u0000", "Expecting value: line 1 column 1 (char 0)"},
	{"[\u0000]", "Expecting value: line 1 column 2 (char 1)"},
	{"\"a\u0000b\"", "Invalid control character at: line 1 column 3 (char 2)"},
	{"\"\\ud83d", "Unterminated string starting at: line 1 column 1 (char 0)"},
	{"\"\\ud83d\\ude0", "Invalid \\uXXXX escape: line 1 column 9 (char 8)"},
	{"\"ab\\u00zz\"", "Invalid \\uXXXX escape: line 1 column 5 (char 4)"},
	{"\"\\u00\"", "Invalid \\uXXXX escape: line 1 column 3 (char 2)"},
	{"[1, ]", "Illegal trailing comma before end of array: line 1 column 3 (char 2)"},
	{"{\"a\":1, }", "Illegal trailing comma before end of object: line 1 column 7 (char 6)"},
	{"{\"a\": 1,\u000a\u000a  ,}", "Expecting property name enclosed in double quotes: line 3 column 3 (char 12)"},
	{"\"\\ude00\\u12\"", "Invalid \\uXXXX escape: line 1 column 9 (char 8)"},
	{"\u00e9\u000a\"x\\q\"", "Expecting value: line 1 column 1 (char 0)"},
	{"[\u000a\"\u00e9\",\u000a\"\u00fc\" 1]", "Expecting ',' delimiter: line 3 column 5 (char 11)"},
}

func TestJSONLoadsErrorText(t *testing.T) {
	for _, tc := range jsonErrorCorpus {
		_, err := JSONLoads([]byte(tc.doc))
		if err == nil || err.Error() != tc.want {
			t.Errorf("JSONLoads(%q) error = %v, want %q", tc.doc, err, tc.want)
		}
	}
}

func TestJSONErrorClass(t *testing.T) {
	_, err := JSONLoads([]byte(`{`))
	var je JSONError
	if !errors.As(err, &je) || je.PyClass() != "JSONDecodeError" || je.PyModule() != "json.decoder" {
		t.Fatalf("err = %#v", err)
	}
	if got := UncaughtText(err); got == "" {
		t.Error("UncaughtText is empty")
	}
}
