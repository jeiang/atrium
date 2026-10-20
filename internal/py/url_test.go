package py

import (
	"reflect"
	"testing"
)

type urlCase struct {
	in      string
	split   []string
	err     string
	host    string
	hasHost bool
	port    int
	hasPort bool
	portErr string
}

var urlSplitCases = []urlCase{
	{in: "\t//ftp[fe80::1%eth0][fe80::1%25eth0]ftp", err: "Invalid IPv6 URL"},
	{in: "\r// \u2100\u00fc", err: "netloc ' \u2100\u00fc' contains invalid characters under NFKC normalization"},
	{in: " \u0000x+y://1a\u001f\u2100", err: "netloc '1a\u001f\u2100' contains invalid characters under NFKC normalization"},
	{in: " //:99999https?\u2100\u2100", split: []string{"", ":99999https", "", "\u2100\u2100", ""}, portErr: "Port could not be cast to integer value as '99999https'"},
	{in: " //ftp\\:\u2100", err: "netloc 'ftp\\:\u2100' contains invalid characters under NFKC normalization"},
	{in: " //\u2100\r:0x;p=1\u0000", err: "netloc '\u2100:0x;p=1\u0000' contains invalid characters under NFKC normalization"},
	{in: "//\n:abc//other.test/p[fe80::1%eth0][::1]:", split: []string{"", ":abc", "//other.test/p[fe80::1%eth0][::1]:", "", ""}, portErr: "Port could not be cast to integer value as 'abc'"},
	{in: "//\n:user:pw ", split: []string{"", ":user:pw ", "", "", ""}, portErr: "Port could not be cast to integer value as 'user:pw '"},
	{in: "//:../up:", split: []string{"", ":..", "/up:", "", ""}, portErr: "Port could not be cast to integer value as '..'"},
	{in: "//:..\u00fc", split: []string{"", ":..\u00fc", "", "", ""}, portErr: "Port could not be cast to integer value as '..\u00fc'"},
	{in: "//:0./cur#", split: []string{"", ":0.", "/cur", "", ""}, portErr: "Port could not be cast to integer value as '0.'"},
	{in: "//:99999https[1.2.3.4]\u2100\u0000@", err: "'' does not appear to be an IPv4 or IPv6 address"},
	{in: "//:99999\uff45://", split: []string{"", ":99999\uff45:", "//", "", ""}, portErr: "Port could not be cast to integer value as '99999\uff45:'"},
	{in: "//:abc:0./cur[::1]", split: []string{"", ":abc:0.", "/cur[::1]", "", ""}, portErr: "Port could not be cast to integer value as 'abc:0.'"},
	{in: "//:abc:;https:99999\r", split: []string{"", ":abc:;https:99999", "", "", ""}, portErr: "Port could not be cast to integer value as 'abc:;https:99999'"},
	{in: "//;\u2100\t", err: "netloc ';\u2100' contains invalid characters under NFKC normalization"},
	{in: "//@\u2100a b.:abc", err: "netloc '@\u2100a b.:abc' contains invalid characters under NFKC normalization"},
	{in: "//Example.TEST\u2100a/b/./x/../y%", err: "netloc 'Example.TEST\u2100a' contains invalid characters under NFKC normalization"},
	{in: "//[1.2.3.4]", err: "An IPv4 address cannot be in brackets"},
	{in: "//a b\u2100\t\t", err: "netloc 'a b\u2100' contains invalid characters under NFKC normalization"},
	{in: "//user:pw#fragpw:0example.test\u00e9.test", split: []string{"", "user:pw", "", "", "fragpw:0example.test\u00e9.test"}, host: "user", hasHost: true, portErr: "Port could not be cast to integer value as 'pw'"},
	{in: "//user:pw./curhttp\r[", split: []string{"", "user:pw.", "/curhttp[", "", ""}, host: "user", hasHost: true, portErr: "Port could not be cast to integer value as 'pw.'"},
	{in: "//user:pwExample.TEST", split: []string{"", "user:pwExample.TEST", "", "", ""}, host: "user", hasHost: true, portErr: "Port could not be cast to integer value as 'pwExample.TEST'"},
	{in: "//user:pwexample.test:", split: []string{"", "user:pwexample.test:", "", "", ""}, host: "user", hasHost: true, portErr: "Port could not be cast to integer value as 'pwexample.test:'"},
	{in: "//x;p=1.\u21001aexample.test\u001f", err: "netloc 'x;p=1.\u21001aexample.test\u001f' contains invalid characters under NFKC normalization"},
	{in: "//\u2100\u0000http%?q", err: "netloc '\u2100\u0000http%' contains invalid characters under NFKC normalization"},
	{in: "//\u2100:99999file%", err: "netloc '\u2100:99999file%' contains invalid characters under NFKC normalization"},
	{in: "//\u2100a b", err: "netloc '\u2100a b' contains invalid characters under NFKC normalization"},
	{in: "//\u2100ftp", err: "netloc '\u2100ftp' contains invalid characters under NFKC normalization"},
	{in: "Example.TEST://\uff45\u2100", err: "netloc '\uff45\u2100' contains invalid characters under NFKC normalization"},
	{in: "https://a b:99999:abc1a", split: []string{"https", "a b:99999:abc1a", "", "", ""}, host: "a b", hasHost: true, portErr: "Port could not be cast to integer value as '99999:abc1a'"},
	{in: "x+y://:80Example.TEST\uff45", split: []string{"x+y", ":80Example.TEST\uff45", "", "", ""}, portErr: "Port could not be cast to integer value as '80Example.TEST\uff45'"},
	{in: "file:///etc/passwd", split: []string{"file", "", "/etc/passwd", "", ""}},
	{in: "http:///x", split: []string{"http", "", "/x", "", ""}},
	{in: "mailto:a@b", split: []string{"mailto", "", "a@b", "", ""}},
}

type joinCase struct{ base, in, out, err string }

var urlJoinCases = []joinCase{
	{base: "http://example.test/a/b/c?x=1#f", in: "ok?x=1#frag", out: "http://example.test/a/b/ok?x=1#frag"},
	{base: "http://example.test/a/b/c?x=1#f", in: "/ok", out: "http://example.test/ok"},
	{base: "http://example.test/a/b/c?x=1#f", in: "//other.test/ok", out: "http://other.test/ok"},
	{base: "http://example.test/a/b/c?x=1#f", in: "file:///etc/passwd", out: "file:///etc/passwd"},
	{base: "http://example.test/a/b/c?x=1#f", in: "ftp://example.test/x", out: "ftp://example.test/x"},
	{base: "http://example.test/a/b/c?x=1#f", in: "../..", out: "http://example.test/"},
	{base: "http://example.test/a/b/c?x=1#f", in: "..", out: "http://example.test/a/"},
	{base: "http://example.test/a/b/c?x=1#f", in: "../../../x", out: "http://example.test/x"},
	{base: "http://example.test/a/b/c?x=1#f", in: "./", out: "http://example.test/a/b/"},
	{base: "http://example.test/a/b/c?x=1#f", in: "?z", out: "http://example.test/a/b/c?z"},
	{base: "http://example.test/a/b/c?x=1#f", in: "#only", out: "http://example.test/a/b/c?x=1#only"},
	{base: "http://example.test/a/b/c?x=1#f", in: "http:other", out: "http://example.test/a/b/other"},
	{base: "http://example.test/a/b/c?x=1#f", in: "https:x/y", out: "https:x/y"},
	{base: "http://example.test/a/b/c?x=1#f", in: "a/./b/../c", out: "http://example.test/a/b/a/c"},
	{base: "http://example.test/a/b/c?x=1#f", in: "g;x?y#s", out: "http://example.test/a/b/g;x?y#s"},
	{base: "http://example.test/a/b/c?x=1#f", in: "//[bad", err: "Invalid IPv6 URL"},
	{base: "http://example.test/a/b/c?x=1#f", in: "http://[::1]x/", err: "Invalid IPv6 URL"},
	{base: "http://example.test", in: "ok?x=1#frag", out: "http://example.test/ok?x=1#frag"},
	{base: "http://example.test", in: "/ok", out: "http://example.test/ok"},
	{base: "http://example.test", in: "//other.test/ok", out: "http://other.test/ok"},
	{base: "http://example.test", in: "file:///etc/passwd", out: "file:///etc/passwd"},
	{base: "http://example.test", in: "ftp://example.test/x", out: "ftp://example.test/x"},
	{base: "http://example.test", in: "../..", out: "http://example.test/"},
	{base: "http://example.test", in: "..", out: "http://example.test/"},
	{base: "http://example.test", in: "../../../x", out: "http://example.test/x"},
	{base: "http://example.test", in: "./", out: "http://example.test/"},
	{base: "http://example.test", in: "?z", out: "http://example.test?z"},
	{base: "http://example.test", in: "#only", out: "http://example.test#only"},
	{base: "http://example.test", in: "http:other", out: "http://example.test/other"},
	{base: "http://example.test", in: "https:x/y", out: "https:x/y"},
	{base: "http://example.test", in: "a/./b/../c", out: "http://example.test/a/c"},
	{base: "http://example.test", in: "g;x?y#s", out: "http://example.test/g;x?y#s"},
	{base: "http://example.test", in: "//[bad", err: "Invalid IPv6 URL"},
	{base: "http://example.test", in: "http://[::1]x/", err: "Invalid IPv6 URL"},
	{base: "http://example.test/a/b/", in: "ok?x=1#frag", out: "http://example.test/a/b/ok?x=1#frag"},
	{base: "http://example.test/a/b/", in: "/ok", out: "http://example.test/ok"},
	{base: "http://example.test/a/b/", in: "//other.test/ok", out: "http://other.test/ok"},
	{base: "http://example.test/a/b/", in: "file:///etc/passwd", out: "file:///etc/passwd"},
	{base: "http://example.test/a/b/", in: "ftp://example.test/x", out: "ftp://example.test/x"},
	{base: "http://example.test/a/b/", in: "../..", out: "http://example.test/"},
	{base: "http://example.test/a/b/", in: "..", out: "http://example.test/a/"},
	{base: "http://example.test/a/b/", in: "../../../x", out: "http://example.test/x"},
	{base: "http://example.test/a/b/", in: "./", out: "http://example.test/a/b/"},
	{base: "http://example.test/a/b/", in: "?z", out: "http://example.test/a/b/?z"},
	{base: "http://example.test/a/b/", in: "#only", out: "http://example.test/a/b/#only"},
	{base: "http://example.test/a/b/", in: "http:other", out: "http://example.test/a/b/other"},
	{base: "http://example.test/a/b/", in: "https:x/y", out: "https:x/y"},
	{base: "http://example.test/a/b/", in: "a/./b/../c", out: "http://example.test/a/b/a/c"},
	{base: "http://example.test/a/b/", in: "g;x?y#s", out: "http://example.test/a/b/g;x?y#s"},
	{base: "http://example.test/a/b/", in: "//[bad", err: "Invalid IPv6 URL"},
	{base: "http://example.test/a/b/", in: "http://[::1]x/", err: "Invalid IPv6 URL"},
	{base: "file:///etc/x", in: "ok?x=1#frag", out: "file:///etc/ok?x=1#frag"},
	{base: "file:///etc/x", in: "/ok", out: "file:///ok"},
	{base: "file:///etc/x", in: "//other.test/ok", out: "file://other.test/ok"},
	{base: "file:///etc/x", in: "file:///etc/passwd", out: "file:///etc/passwd"},
	{base: "file:///etc/x", in: "ftp://example.test/x", out: "ftp://example.test/x"},
	{base: "file:///etc/x", in: "../..", out: "file:///"},
	{base: "file:///etc/x", in: "..", out: "file:///"},
	{base: "file:///etc/x", in: "../../../x", out: "file:///x"},
	{base: "file:///etc/x", in: "./", out: "file:///etc/"},
	{base: "file:///etc/x", in: "?z", out: "file:///etc/x?z"},
	{base: "file:///etc/x", in: "#only", out: "file:///etc/x#only"},
	{base: "file:///etc/x", in: "http:other", out: "http:other"},
	{base: "file:///etc/x", in: "https:x/y", out: "https:x/y"},
	{base: "file:///etc/x", in: "a/./b/../c", out: "file:///etc/a/c"},
	{base: "file:///etc/x", in: "g;x?y#s", out: "file:///etc/g;x?y#s"},
	{base: "file:///etc/x", in: "//[bad", err: "Invalid IPv6 URL"},
	{base: "file:///etc/x", in: "http://[::1]x/", err: "Invalid IPv6 URL"},
	{base: "http://[::1]/a", in: "ok?x=1#frag", out: "http://[::1]/ok?x=1#frag"},
	{base: "http://[::1]/a", in: "/ok", out: "http://[::1]/ok"},
	{base: "http://[::1]/a", in: "//other.test/ok", out: "http://other.test/ok"},
	{base: "http://[::1]/a", in: "file:///etc/passwd", out: "file:///etc/passwd"},
	{base: "http://[::1]/a", in: "ftp://example.test/x", out: "ftp://example.test/x"},
	{base: "http://[::1]/a", in: "../..", out: "http://[::1]/"},
	{base: "http://[::1]/a", in: "..", out: "http://[::1]/"},
	{base: "http://[::1]/a", in: "../../../x", out: "http://[::1]/x"},
	{base: "http://[::1]/a", in: "./", out: "http://[::1]/"},
	{base: "http://[::1]/a", in: "?z", out: "http://[::1]/a?z"},
	{base: "http://[::1]/a", in: "#only", out: "http://[::1]/a#only"},
	{base: "http://[::1]/a", in: "http:other", out: "http://[::1]/other"},
	{base: "http://[::1]/a", in: "https:x/y", out: "https:x/y"},
	{base: "http://[::1]/a", in: "a/./b/../c", out: "http://[::1]/a/c"},
	{base: "http://[::1]/a", in: "g;x?y#s", out: "http://[::1]/g;x?y#s"},
	{base: "http://[::1]/a", in: "//[bad", err: "Invalid IPv6 URL"},
	{base: "http://[::1]/a", in: "http://[::1]x/", err: "Invalid IPv6 URL"},
	{base: "//h/p", in: "ok?x=1#frag", out: "//h/ok?x=1#frag"},
	{base: "//h/p", in: "/ok", out: "//h/ok"},
	{base: "//h/p", in: "//other.test/ok", out: "//other.test/ok"},
	{base: "//h/p", in: "file:///etc/passwd", out: "file:///etc/passwd"},
	{base: "//h/p", in: "ftp://example.test/x", out: "ftp://example.test/x"},
	{base: "//h/p", in: "../..", out: "//h/"},
	{base: "//h/p", in: "..", out: "//h/"},
	{base: "//h/p", in: "../../../x", out: "//h/x"},
	{base: "//h/p", in: "./", out: "//h/"},
	{base: "//h/p", in: "?z", out: "//h/p?z"},
	{base: "//h/p", in: "#only", out: "//h/p#only"},
	{base: "//h/p", in: "http:other", out: "http:other"},
	{base: "//h/p", in: "https:x/y", out: "https:x/y"},
	{base: "//h/p", in: "a/./b/../c", out: "//h/a/c"},
	{base: "//h/p", in: "g;x?y#s", out: "//h/g;x?y#s"},
	{base: "//h/p", in: "//[bad", err: "Invalid IPv6 URL"},
	{base: "//h/p", in: "http://[::1]x/", err: "Invalid IPv6 URL"},
	{base: "x/y", in: "ok?x=1#frag", out: "x/ok?x=1#frag"},
	{base: "x/y", in: "/ok", out: "/ok"},
	{base: "x/y", in: "//other.test/ok", out: "//other.test/ok"},
	{base: "x/y", in: "file:///etc/passwd", out: "file:///etc/passwd"},
	{base: "x/y", in: "ftp://example.test/x", out: "ftp://example.test/x"},
	{base: "x/y", in: "../..", out: "/"},
	{base: "x/y", in: "..", out: "/"},
	{base: "x/y", in: "../../../x", out: "x"},
	{base: "x/y", in: "./", out: "x/"},
	{base: "x/y", in: "?z", out: "x/y?z"},
	{base: "x/y", in: "#only", out: "x/y#only"},
	{base: "x/y", in: "http:other", out: "http:other"},
	{base: "x/y", in: "https:x/y", out: "https:x/y"},
	{base: "x/y", in: "a/./b/../c", out: "x/a/c"},
	{base: "x/y", in: "g;x?y#s", out: "x/g;x?y#s"},
	{base: "x/y", in: "//[bad", err: "Invalid IPv6 URL"},
	{base: "x/y", in: "http://[::1]x/", err: "Invalid IPv6 URL"},
	{base: "mailto:a@b", in: "ok?x=1#frag", out: "ok?x=1#frag"},
	{base: "mailto:a@b", in: "/ok", out: "/ok"},
	{base: "mailto:a@b", in: "//other.test/ok", out: "//other.test/ok"},
	{base: "mailto:a@b", in: "file:///etc/passwd", out: "file:///etc/passwd"},
	{base: "mailto:a@b", in: "ftp://example.test/x", out: "ftp://example.test/x"},
	{base: "mailto:a@b", in: "../..", out: "../.."},
	{base: "mailto:a@b", in: "..", out: ".."},
	{base: "mailto:a@b", in: "../../../x", out: "../../../x"},
	{base: "mailto:a@b", in: "./", out: "./"},
	{base: "mailto:a@b", in: "?z", out: "?z"},
	{base: "mailto:a@b", in: "#only", out: "#only"},
	{base: "mailto:a@b", in: "http:other", out: "http:other"},
	{base: "mailto:a@b", in: "https:x/y", out: "https:x/y"},
	{base: "mailto:a@b", in: "a/./b/../c", out: "a/./b/../c"},
	{base: "mailto:a@b", in: "g;x?y#s", out: "g;x?y#s"},
	{base: "mailto:a@b", in: "//[bad", err: "Invalid IPv6 URL"},
	{base: "mailto:a@b", in: "http://[::1]x/", err: "Invalid IPv6 URL"},
}

func TestURLSplit(t *testing.T) {
	for _, c := range urlSplitCases {
		p, err := URLSplit(c.in)
		if c.err != "" {
			if err == nil || err.Error() != c.err {
				t.Errorf("URLSplit(%q) error %v, want %q", c.in, err, c.err)
			}
			continue
		}
		if err != nil {
			t.Errorf("URLSplit(%q): %v", c.in, err)
			continue
		}
		if got := []string{p.Scheme, p.Netloc, p.Path, p.Query, p.Fragment}; !reflect.DeepEqual(got, c.split) {
			t.Errorf("URLSplit(%q) = %q, want %q", c.in, got, c.split)
		}
		if host, ok := p.Hostname(); host != c.host || ok != c.hasHost {
			t.Errorf("Hostname(%q) = %q, %v", c.in, host, ok)
		}
		port, ok, err := p.Port()
		if c.portErr != "" {
			if err == nil || err.Error() != c.portErr {
				t.Errorf("Port(%q) error %v, want %q", c.in, err, c.portErr)
			}
		} else if err != nil || port != c.port || ok != c.hasPort {
			t.Errorf("Port(%q) = %d, %v, %v", c.in, port, ok, err)
		}
	}
}

func TestURLJoin(t *testing.T) {
	for _, c := range urlJoinCases {
		got, err := URLJoin(c.base, c.in)
		if c.err != "" {
			if err == nil || err.Error() != c.err {
				t.Errorf("URLJoin(%q, %q) error %v, want %q", c.base, c.in, err, c.err)
			}
		} else if err != nil || got != c.out {
			t.Errorf("URLJoin(%q, %q) = %q, %v, want %q", c.base, c.in, got, err, c.out)
		}
	}
}
