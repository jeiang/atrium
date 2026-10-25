package web

import (
	"errors"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"github.com/jeiang/atrium/internal/auth/oidc"
	"github.com/jeiang/atrium/internal/logx"
)

const (
	WizardPath = "/settings/auth"
	testCookie = "atrium_oidc_test"
	testPath   = WizardPath + "/test"
)

var stepToken = regexp.MustCompile(`\{(redirect|access|admin)\}`)

type partView struct {
	Text    string `json:"text"`
	Chip    string `json:"chip"`
	ID      string `json:"id"`
	Missing bool   `json:"missing"`
}

type stepView struct {
	Parts []partView `json:"parts"`
}

type pinnedView struct {
	Issuer        bool `json:"issuer"`
	ClientID      bool `json:"client_id"`
	Scopes        bool `json:"scopes"`
	GroupsClaim   bool `json:"groups_claim"`
	AccessGroup   bool `json:"access_group"`
	AdminGroup    bool `json:"admin_group"`
	AccessEmails  bool `json:"access_emails"`
	AdminEmails   bool `json:"admin_emails"`
	AccessDomains bool `json:"access_domains"`
	AdminDomains  bool `json:"admin_domains"`
}

type setupView struct {
	Preset        string     `json:"preset"`
	Steps         []stepView `json:"steps"`
	IssuerLabel   string     `json:"issuer_label"`
	Issuer        string     `json:"issuer"`
	ClientID      string     `json:"client_id"`
	Scopes        string     `json:"scopes"`
	GroupsClaim   string     `json:"groups_claim"`
	AccessGroup   string     `json:"access_group"`
	AdminGroup    string     `json:"admin_group"`
	AccessEmails  string     `json:"access_emails"`
	AdminEmails   string     `json:"admin_emails"`
	AccessDomains string     `json:"access_domains"`
	AdminDomains  string     `json:"admin_domains"`
	Groups        bool       `json:"groups"`
	Lists         bool       `json:"lists"`
	Placeholder   string     `json:"placeholder"`
	Open          bool       `json:"open"`
	SecretSet     bool       `json:"secret_set"`
	SecretPinned  bool       `json:"secret_pinned"`
	Pinned        pinnedView `json:"pinned"`
}

type presetOption struct {
	Name     string `json:"name"`
	Label    string `json:"label"`
	Selected bool   `json:"selected"`
}

type stateView struct {
	Name   string   `json:"name"`
	Active bool     `json:"active"`
	Modes  []string `json:"modes"`
}

type testView struct {
	Status      string   `json:"status"`
	Reason      string   `json:"reason"`
	Groups      []string `json:"groups"`
	Fingerprint string   `json:"fingerprint"`
	Seq         int      `json:"seq"`
	Waiting     bool     `json:"waiting"`
	OOB         bool     `json:"oob"`
}

type wizardData struct {
	Presets      []presetOption `json:"presets"`
	PresetPinned bool           `json:"preset_pinned"`
	Setup        setupView      `json:"setup"`
	State        stateView      `json:"state"`
	Test         testView       `json:"test"`
	OOB          bool           `json:"oob"`
}

type resultView struct {
	Passed bool     `json:"passed"`
	Reason string   `json:"reason"`
	Groups []string `json:"groups"`
}

func (s *Server) steps(v oidc.View) []stepView {
	chips := map[string]string{
		"redirect": "",
		"access":   v.Values[oidc.KeyAccessGroup],
		"admin":    v.Values[oidc.KeyAdminGroup],
	}
	if v.Public != "" {
		chips["redirect"] = v.Public + CallbackPath
	}
	if chips["access"] == "" {
		chips["access"] = oidc.DefaultAccessGroup
	}
	if chips["admin"] == "" {
		chips["admin"] = oidc.DefaultAdminGroup
	}
	var out []stepView
	for i, text := range v.Preset.Steps {
		var step stepView
		rest := text
		for j := 0; ; j++ {
			loc := stepToken.FindStringSubmatchIndex(rest)
			if loc == nil {
				break
			}
			if loc[0] > 0 {
				step.Parts = append(step.Parts, partView{Text: rest[:loc[0]]})
			}
			name := rest[loc[2]:loc[3]]
			part := partView{Chip: chips[name], ID: "copy-" + strconv.Itoa(i) + "-" + strconv.Itoa(j)}
			if name == "redirect" && part.Chip == "" {
				part = partView{Missing: true}
			}
			step.Parts = append(step.Parts, part)
			rest = rest[loc[1]:]
		}
		if rest != "" {
			step.Parts = append(step.Parts, partView{Text: rest})
		}
		out = append(out, step)
	}
	return out
}

func (s *Server) setupFor(v oidc.View) setupView {
	p, val := v.Preset, v.Values
	pin := func(key string) bool { return v.Pinned[key] }
	view := setupView{
		Preset: p.Name, Steps: s.steps(v), IssuerLabel: p.IssuerLabel, Issuer: val[oidc.KeyIssuer],
		ClientID: val[oidc.KeyClientID], Scopes: val[oidc.KeyScopes], GroupsClaim: val[oidc.KeyGroupsClaim],
		AccessGroup: val[oidc.KeyAccessGroup], AdminGroup: val[oidc.KeyAdminGroup],
		AccessEmails: val[oidc.KeyAccessEmails], AdminEmails: val[oidc.KeyAdminEmails],
		AccessDomains: val[oidc.KeyAccessDomains], AdminDomains: val[oidc.KeyAdminDomains],
		Groups: p.Groups, Lists: p.Lists, Placeholder: p.Placeholder, Open: p.Open,
		SecretSet: v.SecretSet, SecretPinned: v.SecretPinned,
		Pinned: pinnedView{
			Issuer: pin(oidc.KeyIssuer), ClientID: pin(oidc.KeyClientID), Scopes: pin(oidc.KeyScopes), GroupsClaim: pin(oidc.KeyGroupsClaim),
			AccessGroup: pin(oidc.KeyAccessGroup), AdminGroup: pin(oidc.KeyAdminGroup), AccessEmails: pin(oidc.KeyAccessEmails),
			AdminEmails: pin(oidc.KeyAdminEmails), AccessDomains: pin(oidc.KeyAccessDomains), AdminDomains: pin(oidc.KeyAdminDomains),
		},
	}
	return view
}

func (s *Server) stateFor(v oidc.View) stateView {
	name := "No provider"
	if v.Active {
		if p, ok := oidc.PresetNamed(v.Values[oidc.KeyPreset]); ok {
			name = p.Label()
		}
	}
	modes := []string{"OIDC"}
	if s.auth.Proxy {
		modes = append(modes, "Proxy")
	}
	return stateView{Name: name, Active: v.Active, Modes: modes}
}

func testFrom(res oidc.TestResult, after string) testView {
	view := testView{Status: string(res.Status), Reason: res.Reason, Groups: res.Groups, Fingerprint: res.Fingerprint, Seq: res.Seq}
	if after != "" {
		if n, err := strconv.Atoi(after); err == nil && res.Seq <= n {
			view.Status, view.Waiting = string(oidc.TestPending), true
		}
	}
	return view
}

func (s *Server) wizardData(r *http.Request, presetName string) (wizardData, error) {
	v, err := s.oidc.View(r.Context(), presetName)
	if err != nil {
		return wizardData{}, err
	}
	options := make([]presetOption, 0, len(oidc.Presets()))
	for _, p := range oidc.Presets() {
		options = append(options, presetOption{Name: p.Name, Label: p.Label(), Selected: p.Name == v.Preset.Name})
	}
	return wizardData{
		Presets: options, PresetPinned: v.PresetPinned, Setup: s.setupFor(v), State: s.stateFor(v),
		Test: testFrom(s.oidc.TestResult(), ""),
	}, nil
}

func (s *Server) wizardFail(w http.ResponseWriter, r *http.Request, err error) {
	log.Error("sign-in settings failed", logx.Err, err.Error())
	s.Error(w, r, http.StatusInternalServerError, "Internal Server Error")
}

func (s *Server) wizardRender(w http.ResponseWriter, r *http.Request, status int, name string, data wizardData) {
	f, err := s.render.Frame(r, "settings", data)
	if err == nil {
		if strings.HasPrefix(name, "_") {
			err = s.render.Fragment(w, r, status, name, f)
		} else {
			err = s.render.Page(w, r, status, name, f)
		}
	}
	if err != nil {
		s.wizardFail(w, r, err)
	}
}

func (s *Server) wizardPage(w http.ResponseWriter, r *http.Request) {
	noStore(w)
	data, err := s.wizardData(r, "")
	if err != nil {
		s.wizardFail(w, r, err)
		return
	}
	s.wizardRender(w, r, http.StatusOK, "settings_auth.gohtml", data)
}

func (s *Server) wizardPreset(w http.ResponseWriter, r *http.Request) {
	noStore(w)
	data, err := s.wizardData(r, r.URL.Query().Get("preset"))
	if err != nil {
		s.wizardFail(w, r, err)
		return
	}
	data.Test = testView{Status: string(oidc.TestIdle), Seq: s.oidc.TestResult().Seq, OOB: true}
	s.wizardRender(w, r, http.StatusOK, "_oidc_preset.gohtml", data)
}

func (s *Server) wizardStatus(w http.ResponseWriter, r *http.Request) {
	noStore(w)
	data := wizardData{Test: testFrom(s.oidc.TestResult(), r.URL.Query().Get("after"))}
	s.wizardRender(w, r, http.StatusOK, "_oidc_test.gohtml", data)
}

func (s *Server) wizardActivate(w http.ResponseWriter, r *http.Request) {
	noStore(w)
	err := s.oidc.Activate(r.Context(), FormString(FormValues(r), "fingerprint", ""))
	switch {
	case err == nil:
		v, verr := s.oidc.View(r.Context(), "")
		if verr != nil {
			s.wizardFail(w, r, verr)
			return
		}
		data := wizardData{State: s.stateFor(v), Test: testView{Status: string(oidc.TestIdle), Seq: s.oidc.TestResult().Seq}, OOB: true}
		s.wizardRender(w, r, http.StatusOK, "_oidc_activated.gohtml", data)
	case errors.Is(err, oidc.ErrNoPassedTest), errors.Is(err, oidc.ErrStaleTest):
		data := wizardData{Test: testView{Status: string(oidc.TestFailed), Reason: "Test sign-in again before activating.", Seq: s.oidc.TestResult().Seq}}
		s.wizardRender(w, r, http.StatusConflict, "_oidc_test.gohtml", data)
	default:
		var field *oidc.FieldError
		if errors.As(err, &field) {
			data := wizardData{Test: testView{Status: string(oidc.TestFailed), Reason: "The settings were not saved.", Seq: s.oidc.TestResult().Seq}}
			s.wizardRender(w, r, http.StatusUnprocessableEntity, "_oidc_test.gohtml", data)
			return
		}
		s.wizardFail(w, r, err)
	}
}

func formFor(r *http.Request) oidc.Form {
	form := FormValues(r)
	get := func(name string) string { return FormString(form, name, "") }
	return oidc.Form{
		Preset: get("preset"), Issuer: get("issuer"), ClientID: get("client_id"), ClientSecret: get("client_secret"),
		Scopes: get("scopes"), GroupsClaim: get("groups_claim"), AccessGroup: get("access_group"), AdminGroup: get("admin_group"),
		AccessEmails: get("access_emails"), AdminEmails: get("admin_emails"), AccessDomains: get("access_domains"), AdminDomains: get("admin_domains"),
	}
}

func (s *Server) wizardTest(w http.ResponseWriter, r *http.Request) {
	noStore(w)
	draft, err := s.oidc.Draft(r.Context(), formFor(r))
	if err != nil {
		var config *oidc.ConfigError
		var field *oidc.FieldError
		reason := "The test could not start."
		switch {
		case errors.Is(err, oidc.ErrNeedsCipher):
			reason = "The database is not encrypted, so the client secret cannot be stored."
		case errors.As(err, &field):
			reason = "Fields missing."
		case errors.As(err, &config):
			reason = "Fields missing."
		default:
			log.Error("sign-in test could not start", logx.Err, err.Error())
		}
		res := s.oidc.RejectTest(http.StatusUnprocessableEntity, reason)
		s.testResultPage(w, r, res)
		return
	}
	login, err := s.oidc.BeginTest(r.Context(), draft)
	if err != nil {
		s.testResultPage(w, r, s.oidc.TestResult())
		return
	}
	setCookie(w, testCookie, login.Binding, 600, s.secure(r))
	w.Header().Set("Refresh", "0; url="+login.URL)
	s.testWaitPage(w, r, login.URL)
}

func (s *Server) testWaitPage(w http.ResponseWriter, r *http.Request, target string) {
	f, err := s.render.Frame(r, "login", map[string]any{"url": target})
	if err == nil {
		err = s.render.Page(w, r, http.StatusOK, "auth_test_wait.gohtml", f)
	}
	if err != nil {
		s.wizardFail(w, r, err)
	}
}

func (s *Server) testResultPage(w http.ResponseWriter, r *http.Request, res oidc.TestResult) {
	status := res.HTTP
	if res.Status == oidc.TestPassed || status == 0 {
		status = http.StatusOK
	}
	view := resultView{Passed: res.Status == oidc.TestPassed, Reason: res.Reason, Groups: res.Groups}
	f, err := s.render.Frame(r, "login", view)
	if err == nil {
		err = s.render.Page(w, r, status, "auth_test_result.gohtml", f)
	}
	if err != nil {
		s.wizardFail(w, r, err)
	}
}

func (s *Server) testCallback(w http.ResponseWriter, r *http.Request) {
	noStore(w)
	q := r.URL.Query()
	binding := cookieValue(r, testCookie, s.secure(r))
	expireCookie(w, testCookie, s.secure(r))
	res := s.oidc.CompleteTest(r.Context(), q.Get("state"), q.Get("code"), q.Get("error"), binding)
	s.testResultPage(w, r, res)
}
