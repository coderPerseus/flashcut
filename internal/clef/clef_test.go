package clef

import "testing"

func TestParse(t *testing.T) {
	bare := `{"model":"clef-flash","answers":{"x":{"type":"noul","noul":0.9}},"usage":{"input_tokens":12,"output_tokens":0}}`
	wrapped := `{"result":` + bare + `,"success":true,"errors":[]}`
	for _, body := range []string{bare, wrapped} {
		r, err := parse([]byte(body))
		if err != nil || r.Answers["x"].Noul != 0.9 || r.Usage.InputTokens != 12 {
			t.Fatalf("parse(%s) = %+v, %v", body, r, err)
		}
	}
	if _, err := parse([]byte(`{"result":null,"success":false,"errors":[{"message":"quota"}]}`)); err == nil {
		t.Fatal("want an error for success=false")
	}
}
