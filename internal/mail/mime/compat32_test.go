package mime

import "testing"

func TestCompat32Header(t *testing.T) {
	for _, c := range []struct {
		header string
		want   string
		found  bool
	}{
		{"Message-ID: <a@b.test>\r\n\r\n", "<a@b.test>", true},
		{"Message-ID:\r\n <folded@b>\r\n\r\n", "<folded@b>", true},
		{"Message-ID:  \r\n <fo\r\n lded@b>\r\n\r\n", "<fo\r\n lded@b>", true},
		{"Subject: x\r\nmessage-id:   \t<sp@b.test> \r\n\r\n", "<sp@b.test> ", true},
		{"Message-ID: <caf\xc3\xa9@b.test>\r\n\r\n", "<caf\ufffd\ufffd@b.test>", true},
		{"Message-ID:\r\n <f\xe9\r\n x@b>\r\n\r\n", "<f\ufffd\r\n x@b>", true},
		{"Message-ID: \xff  <a@b>\t\r\n\r\n", "\ufffd  <a@b>\t", true},
		{"Message-ID: <=?utf-8?q?a?=@b.test>\r\n\r\n", "<=?utf-8?q?a?=@b.test>", true},
		{"Message-ID: <first@x>\r\nMessage-ID: <second@x>\r\n\r\n", "<first@x>", true},
		{"Message-ID: <a@b>\r\n", "<a@b>", true},
		{"From: a\r\n\r\n", "", false},
		{"X: y\r\nnot a header\r\nMessage-ID: <late@x>\r\n\r\n", "", false},
	} {
		got, found := Compat32Header([]byte(c.header), "Message-ID")
		if got != c.want || found != c.found {
			t.Errorf("Compat32Header(%q) = %q, %v; want %q, %v", c.header, got, found, c.want, c.found)
		}
	}
}
