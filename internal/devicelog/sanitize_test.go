package devicelog

import (
	"net/url"
	"strings"
	"testing"
)

func TestDescribeReplacesBase64WithSize(t *testing.T) {
	photo := strings.Repeat("QUJD", 1000) // 4000 base64 characters
	got := Describe("application/json", []byte(`{"faceURL":"","img":"`+photo+`"}`))
	if strings.Contains(got, "QUJDQUJD") {
		t.Errorf("base64 not replaced: %s", got)
	}
	if !strings.Contains(got, "<base64 3KB>") {
		t.Errorf("want <base64 3KB>: %s", got)
	}
}

// Biometric templates (face, fingerprint) travel as long hex or base64
// strings; they must never reach the log.
func TestDescribeHidesBiometricPayload(t *testing.T) {
	template := strings.Repeat("0a1b2c3d", 128) // 1024 hex characters
	got := Describe("application/json", []byte(`{"FingerPrintData":"`+template+`","fingerPrintID":1}`))
	if strings.Contains(got, "0a1b2c3d0a1b") {
		t.Errorf("biometric payload leaked: %s", got)
	}
	if !strings.Contains(got, `"fingerPrintID":1`) {
		t.Errorf("surrounding fields lost: %s", got)
	}
}

func TestDescribeMasksCredentials(t *testing.T) {
	cases := map[string]struct{ ct, body, want string }{
		"json":          {"application/json", `{"userName":"a","password":"hunter2"}`, `"password":"***"`},
		"json prefixed": {"application/json", `{"CardPassword":"hunter2"}`, `"CardPassword":"***"`},
		"json token":    {"application/json", `{"accessToken":"hunter2"}`, `"accessToken":"***"`},
		"xml":           {"application/xml", `<User><password>hunter2</password></User>`, `<password>***</password>`},
		"xml secret":    {"application/xml", `<Auth><clientSecret>hunter2</clientSecret></Auth>`, `<clientSecret>***</clientSecret>`},
		"form":          {"application/x-www-form-urlencoded", `user=a&pwd=hunter2`, `pwd=***`},
		"form passwd":   {"application/x-www-form-urlencoded", `user=a&passwd=hunter2`, `passwd=***`},
	}
	for name, c := range cases {
		got := Describe(c.ct, []byte(c.body))
		if strings.Contains(got, "hunter2") || !strings.Contains(got, c.want) {
			t.Errorf("%s: %s", name, got)
		}
	}
}

func TestDescribeKeepsNonSecretFields(t *testing.T) {
	body := `{"employeeNo":"7","name":"Alice","userType":"normal"}`
	if got := Describe("application/json", []byte(body)); got != body {
		t.Errorf("got %q, want body unchanged", got)
	}
}

func TestDescribeCompactsToOneLine(t *testing.T) {
	got := Describe("application/xml", []byte("<A>\n  <B>1</B>\n</A>\n"))
	if got != "<A><B>1</B></A>" {
		t.Errorf("got %q", got)
	}
}

func TestDescribeTruncates(t *testing.T) {
	got := Describe("text/plain", []byte(strings.Repeat("a ", 3000)))
	if !strings.HasSuffix(got, cutSuffix) {
		t.Errorf("missing truncation suffix")
	}
	if len(got) > maxBody+len(cutSuffix) {
		t.Errorf("length %d", len(got))
	}
}

func TestDescribeBinary(t *testing.T) {
	if got := Describe("image/jpeg", make([]byte, 48*1024)); got != "<image/jpeg 48KB>" {
		t.Errorf("got %q", got)
	}
	if got := Describe("", []byte{0xff, 0xd8, 0xff, 0x00}); got != "<application/octet-stream 4B>" {
		t.Errorf("got %q", got)
	}
}

const multipartPush = "--MIME_boundary\r\n" +
	"Content-Disposition: form-data; name=\"event_log\"\r\n" +
	"Content-Type: application/json\r\n\r\n" +
	`{"eventType":"AccessControllerEvent","AccessControllerEvent":{"majorEventType":5,"subEventType":75,"cardNo":"999","name":"Alice"}}` + "\r\n" +
	"--MIME_boundary\r\n" +
	"Content-Disposition: form-data; name=\"Picture\"; filename=\"Picture.jpg\"\r\n" +
	"Content-Type: image/jpeg\r\n\r\n" +
	"\xff\xd8\xff\x00\r\n" +
	"--MIME_boundary--\r\n"

func TestDescribeMultipart(t *testing.T) {
	got := Describe("multipart/form-data; boundary=MIME_boundary", []byte(multipartPush))
	if !strings.Contains(got, `[event_log] {"eventType":"AccessControllerEvent"`) {
		t.Errorf("JSON part missing: %s", got)
	}
	if !strings.Contains(got, "[Picture] <image/jpeg 4B>") {
		t.Errorf("image part missing: %s", got)
	}
}

func TestKeyFieldsNestedJSON(t *testing.T) {
	got := KeyFields("application/json",
		[]byte(`{"UserInfo":{"employeeNo":"123","name":"Alice Smith","userType":"normal"}}`), nil)
	if got != `employeeNo=123 name="Alice Smith" userType=normal` {
		t.Errorf("got %q", got)
	}
}

func TestKeyFieldsXMLFormAndQuery(t *testing.T) {
	if got := KeyFields("application/xml", []byte(`<CardInfo><employeeNo>7</employeeNo><cardNo>55</cardNo></CardInfo>`), nil); got != "employeeNo=7 cardNo=55" {
		t.Errorf("xml: %q", got)
	}
	if got := KeyFields("application/x-www-form-urlencoded", []byte("CardNo=9&CardName=Bob"), nil); got != "CardName=Bob CardNo=9" {
		t.Errorf("form: %q", got)
	}
	q := url.Values{"action": {"insert"}, "name": {"AccessUserCard"}, "ignored": {"x"}}
	if got := KeyFields("", nil, q); got != "action=insert name=AccessUserCard" {
		t.Errorf("query: %q", got)
	}
}

func TestKeyFieldsLimits(t *testing.T) {
	// Alphabetical order (upper case first): CardName comes first, so the
	// long value is included and must be truncated.
	body := `{"employeeNo":"1","name":"x","cardNo":"2","UserID":"3","CardName":"` + strings.Repeat("n", 60) + `","major":5,"minor":6,"searchID":"7"}`
	got := KeyFields("application/json", []byte(body), nil)
	if n := len(strings.Fields(got)); n != 6 {
		t.Errorf("%d fields, want 6: %s", n, got)
	}
	if !strings.Contains(got, "CardName="+strings.Repeat("n", 40)+"…") || strings.Contains(got, strings.Repeat("n", 41)) {
		t.Errorf("value not truncated at 40: %s", got)
	}
}

func TestKeyFieldsMultipartWithoutContentType(t *testing.T) {
	got := KeyFields("", []byte(multipartPush), nil)
	if got != "eventType=AccessControllerEvent cardNo=999 majorEventType=5 name=Alice subEventType=75" {
		t.Errorf("got %q", got)
	}
	// Dahua stream part: starts with CRLF and has no closing boundary.
	dahua := "\r\n--myboundary\r\nContent-Type: text/plain\r\nContent-Length: 30\r\n\r\n" +
		`{"Events":[{"Code":"AccessControl","Data":{"UserID":"10"}}]}` + "\r\n"
	if got := KeyFields("", []byte(dahua), nil); got != "Code=AccessControl UserID=10" {
		t.Errorf("dahua: %q", got)
	}
}

func TestUserFromAuth(t *testing.T) {
	cases := map[string]string{
		`Digest username="admin", realm="x", nonce="y"`: "admin",
		"Basic YWRtaW46c2VncmVkbw==":                    "admin",
		"":                                              "",
		"Bearer abc":                                    "",
	}
	for h, want := range cases {
		if got := UserFromAuth(h); got != want {
			t.Errorf("UserFromAuth(%q) = %q, want %q", h, got, want)
		}
	}
}

func TestHumanSize(t *testing.T) {
	cases := map[int]string{0: "0B", 512: "512B", 1024: "1KB", 48 * 1024: "48KB", 3 << 20: "3.0MB"}
	for n, want := range cases {
		if got := humanSize(n); got != want {
			t.Errorf("humanSize(%d) = %q, want %q", n, got, want)
		}
	}
}

func TestDescribeMasksPrefixedPasswordFields(t *testing.T) {
	got := Describe("application/x-www-form-urlencoded", []byte("CardNo=1&CardPassword=abc123&user.Password=xyz789"))
	if strings.Contains(got, "abc123") || strings.Contains(got, "xyz789") {
		t.Errorf("password leaked: %s", got)
	}
}
