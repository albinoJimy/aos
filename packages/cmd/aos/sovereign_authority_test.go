package main

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	govsov "github.com/aos-ref/control-plane/governance/sovereignty"
	oidc "github.com/aos-ref/integration/oidc"
	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	audit "github.com/aos-ref/platform/audit"
)

// AOS-205 — testes FALSIFICÁVEIS da soberania de leitura endurecida. Cada teste é NÃO-VACUOSO:
// prova o DENY do header forjado E o PERMIT da credencial válida correspondente (dois sentidos),
// usando um IdP OIDC de teste em memória (molde de AOS-174) que minta ID-tokens assinados em
// runtime com uma chave EFÉMERA — nunca há segredo em código/fixtures. Correr SEMPRE com -race.

const (
	sovOIDCAudience = "aos-sovereign-reader"
	sovOIDCKid      = "sov-rsa-1"
	sovReaderSub    = "reader:alice@corp.example"
)

// sovFixedNow é o instante determinístico partilhado pelo IdP e pelo verifier do nó.
func sovFixedNow() time.Time { return time.Unix(1_700_000_000, 0).UTC() }

// sovTestIDP é um IdP OIDC de teste em memória: gera uma chave RSA efémera, serve JWKS +
// discovery e minta ID-tokens RS256 com um claim `board`.
type sovTestIDP struct {
	server *httptest.Server
	rsaKey *rsa.PrivateKey
}

func newSovTestIDP(t *testing.T) *sovTestIDP {
	t.Helper()
	rk, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("gerar RSA efemera: %v", err)
	}
	idp := &sovTestIDP{rsaKey: rk}
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{
			"issuer":   idp.server.URL,
			"jwks_uri": idp.server.URL + "/jwks",
		})
	})
	mux.HandleFunc("/jwks", func(w http.ResponseWriter, _ *http.Request) {
		pub := &idp.rsaKey.PublicKey
		eBytes := big2bytes(pub.E)
		_ = json.NewEncoder(w).Encode(map[string]any{"keys": []any{map[string]any{
			"kty": "RSA", "kid": sovOIDCKid, "alg": "RS256", "use": "sig",
			"n": rawB64(pub.N.Bytes()), "e": rawB64(eBytes),
		}}})
	})
	idp.server = httptest.NewServer(mux)
	t.Cleanup(idp.server.Close)
	return idp
}

func (idp *sovTestIDP) issuer() string { return idp.server.URL }

// mintToken minta um ID-token RS256 VÁLIDO com o `sub` e o `board` dados (relativo ao relógio
// fixo). A assinatura é genuína sob a chave efémera do IdP.
func (idp *sovTestIDP) mintToken(t *testing.T, sub, board string) string {
	t.Helper()
	now := sovFixedNow().Unix()
	claims := map[string]any{
		"iss":   idp.issuer(),
		"sub":   sub,
		"aud":   sovOIDCAudience,
		"exp":   now + 3600,
		"iat":   now - 30,
		"nbf":   now - 30,
		"board": board,
	}
	hdr := map[string]any{"alg": "RS256", "typ": "JWT", "kid": sovOIDCKid}
	hb, _ := json.Marshal(hdr)
	pb, _ := json.Marshal(claims)
	input := rawB64(hb) + "." + rawB64(pb)
	digest := sha256.Sum256([]byte(input))
	sig, err := rsa.SignPKCS1v15(rand.Reader, idp.rsaKey, crypto.SHA256, digest[:])
	if err != nil {
		t.Fatalf("assinar token: %v", err)
	}
	return input + "." + rawB64(sig)
}

// mintTokenIat minta um ID-token RS256 VÁLIDO (assinatura genuína) com iat/exp CONTROLADOS
// relativos ao relógio fixo — para provar o tecto de idade (MaxAge) end-to-end: um token cujo
// exp ainda está no futuro mas cujo iat é anterior a now-MaxAge é recusado ([oidc.ErrTokenTooOld]).
func (idp *sovTestIDP) mintTokenIat(t *testing.T, sub, board string, iatOffset, expOffset time.Duration) string {
	t.Helper()
	now := sovFixedNow().Unix()
	claims := map[string]any{
		"iss":   idp.issuer(),
		"sub":   sub,
		"aud":   sovOIDCAudience,
		"exp":   now + int64(expOffset.Seconds()),
		"iat":   now + int64(iatOffset.Seconds()),
		"nbf":   now + int64(iatOffset.Seconds()),
		"board": board,
	}
	hdr := map[string]any{"alg": "RS256", "typ": "JWT", "kid": sovOIDCKid}
	hb, _ := json.Marshal(hdr)
	pb, _ := json.Marshal(claims)
	input := rawB64(hb) + "." + rawB64(pb)
	digest := sha256.Sum256([]byte(input))
	sig, err := rsa.SignPKCS1v15(rand.Reader, idp.rsaKey, crypto.SHA256, digest[:])
	if err != nil {
		t.Fatalf("assinar token: %v", err)
	}
	return input + "." + rawB64(sig)
}

func rawB64(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

func big2bytes(e int) []byte {
	// Codifica o expoente público (tipicamente 65537) em big-endian mínimo.
	if e == 0 {
		return []byte{0}
	}
	var out []byte
	for e > 0 {
		out = append([]byte{byte(e & 0xff)}, out...)
		e >>= 8
	}
	return out
}

// newSovOIDCNode compõe um nó COM a fonte de autoridade board→região (AOS-205) e a CREDENCIAL
// FORTE OIDC ligada ao IdP de teste. O board autorizado é govBoard→govRegion.
func newSovOIDCNode(t *testing.T, model agentruntime.ModelClient, idp *sovTestIDP) *Node {
	t.Helper()
	cfg := tnBaseConfig()
	cfg.Model = model
	cfg.BoardRegions = map[string]string{govBoard: govRegion}
	cfg.SovereignClock = sovFixedNow
	cfg.SovereignReadOIDC = &oidc.Config{
		Issuer:     idp.issuer(),
		Audience:   sovOIDCAudience,
		JWKSURI:    idp.issuer() + "/jwks",
		HTTPClient: idp.server.Client(),
		Clock:      sovFixedNow,
	}
	node, err := Bootstrap(context.Background(), cfg, io.Discard)
	if err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}
	t.Cleanup(func() { _ = node.Close() })
	return node
}

// newSovOIDCNodeMaxAge é como [newSovOIDCNode] mas fixa o tecto de idade (MaxAge) do verificador —
// para provar end-to-end que o wiring de AOS-205 recusa um ID-token de soberania capturado (iat
// antigo, exp ainda válido) quando MaxAge está aplicado, e que o MESMO token passaria com MaxAge=0.
func newSovOIDCNodeMaxAge(t *testing.T, model agentruntime.ModelClient, idp *sovTestIDP, maxAge time.Duration) *Node {
	t.Helper()
	cfg := tnBaseConfig()
	cfg.Model = model
	cfg.BoardRegions = map[string]string{govBoard: govRegion}
	cfg.SovereignClock = sovFixedNow
	cfg.SovereignReadOIDC = &oidc.Config{
		Issuer:     idp.issuer(),
		Audience:   sovOIDCAudience,
		JWKSURI:    idp.issuer() + "/jwks",
		HTTPClient: idp.server.Client(),
		Clock:      sovFixedNow,
		MaxAge:     maxAge,
	}
	node, err := Bootstrap(context.Background(), cfg, io.Discard)
	if err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}
	t.Cleanup(func() { _ = node.Close() })
	return node
}

// TestReadPathMaxAgeBoundsReplayWindow prova END-TO-END o fecho do achado de anti-replay
// (auditoria v4): um ID-token de soberania com exp AINDA VÁLIDO mas iat ANTIGO (o cenário de um
// token legitimamente emitido e capturado) é RECUSADO (404) quando MaxAge está aplicado — e o
// MESMO token é ACEITE (200) num nó com MaxAge=0 (o comportamento ANTIGO). Os dois sentidos
// isolam o MaxAge como a garantia real, não um efeito colateral do exp. Sem MaxAge=0 o token
// passaria pela janela exp inteira; parseSovereignReadOIDC garante que a produção nunca cai nesse 0.
func TestReadPathMaxAgeBoundsReplayWindow(t *testing.T) {
	idp := newSovTestIDP(t)
	// Token com iat 1h no passado mas exp 1h no futuro: válido por exp, ANTIGO por idade.
	staleTok := idp.mintTokenIat(t, sovReaderSub, govBoard, -time.Hour, time.Hour)

	// COM MaxAge=5m ⇒ recusado (ErrTokenTooOld ⇒ 404 uniforme).
	nodeBounded := newSovOIDCNodeMaxAge(t, &countingModel{}, idp, 5*time.Minute)
	svcB, hB := newAPI(t, nodeBounded)
	submitAndWait(t, svcB, "run-maxage")
	den := getReq(hB, "/runs/run-maxage", bearerHeaders(staleTok))
	if den.Code != http.StatusNotFound {
		t.Fatalf("token de soberania ANTIGO (iat -1h) devia ser recusado sob MaxAge=5m (404), veio %d (%s)", den.Code, den.Body.String())
	}

	// COM MaxAge=0 (comportamento antigo) ⇒ o MESMO token passa (prova que é o MaxAge que recusa,
	// não o exp nem a assinatura).
	nodeUnbounded := newSovOIDCNodeMaxAge(t, &countingModel{}, idp, 0)
	svcU, hU := newAPI(t, nodeUnbounded)
	submitAndWait(t, svcU, "run-maxage")
	ok := getReq(hU, "/runs/run-maxage", bearerHeaders(staleTok))
	if ok.Code != http.StatusOK {
		t.Fatalf("controlo: sob MaxAge=0 o MESMO token antigo devia passar (200) — senao o teste seria vacuo; veio %d (%s)", ok.Code, ok.Body.String())
	}
}

// bearerHeaders devolve os headers de leitura com uma credencial forte (Bearer <id-token>).
func bearerHeaders(token string) map[string]string {
	return map[string]string{"Authorization": "Bearer " + token}
}

// TestReadPathForgedBoardRejectedCredentialAccepted é a PROVA FALSIFICÁVEL central de AOS-205
// (anchor duro): com a credencial forte composta, o header X-Aos-Board deixa de autorizar.
//
//   - FORJADO A: header X-Aos-Board=govBoard (board VÁLIDO) mas SEM credencial ⇒ RECUSADO (404).
//     Antes de AOS-205 isto PASSAVA (o header auto-declarado autorizava).
//   - FORJADO B: header X-Aos-Board=govBoard (autorizado) MAS a credencial verificada afirma um
//     board DIFERENTE/desconhecido ⇒ RECUSADO. O board vem da CLAIM, não do header — o header é
//     ignorado. Prova que a decisão não é vacuamente "há Bearer": é a claim que decide.
//   - VÁLIDO: credencial com board=govBoard (o board autorizado) ⇒ ACEITE (200).
//
// Os dois sentidos (deny do forjado, permit do válido) tornam o teste não-vacuo.
func TestReadPathForgedBoardRejectedCredentialAccepted(t *testing.T) {
	idp := newSovTestIDP(t)
	node := newSovOIDCNode(t, &countingModel{}, idp)
	svc, h := newAPI(t, node)
	submitAndWait(t, svc, "run-205")

	// FORJADO A — board válido no header, SEM credencial ⇒ 404.
	forgedNoCred := getReq(h, "/runs/run-205", map[string]string{
		HeaderReaderPrincipal: govReader, HeaderReaderBoard: govBoard,
	})
	if forgedNoCred.Code != http.StatusNotFound {
		t.Fatalf("X-Aos-Board forjado SEM credencial devia dar 404, veio %d (%s)", forgedNoCred.Code, forgedNoCred.Body.String())
	}

	// FORJADO B — header diz o board autorizado, mas a credencial VERIFICADA afirma outro board
	// (desconhecido) ⇒ 404. O header é ignorado; a claim é que decide.
	otherBoardTok := idp.mintToken(t, sovReaderSub, "board:desconhecido-999")
	forgedClaim := getReq(h, "/runs/run-205", map[string]string{
		HeaderReaderBoard: govBoard, // forja o board autorizado no header
		"Authorization":   "Bearer " + otherBoardTok,
	})
	if forgedClaim.Code != http.StatusNotFound {
		t.Fatalf("credencial com board desconhecido (header a forjar govBoard) devia dar 404, veio %d (%s)", forgedClaim.Code, forgedClaim.Body.String())
	}

	// VÁLIDO — credencial com board=govBoard ⇒ 200.
	validTok := idp.mintToken(t, sovReaderSub, govBoard)
	ok := getReq(h, "/runs/run-205", bearerHeaders(validTok))
	if ok.Code != http.StatusOK {
		t.Fatalf("credencial VALIDA (board=govBoard) devia dar 200, veio %d (%s)", ok.Code, ok.Body.String())
	}
	var st runStateResponse
	if err := json.Unmarshal(ok.Body.Bytes(), &st); err != nil || st.Status != "completed" {
		t.Fatalf("resposta 200 devia ser o desfecho completed, veio status=%q err=%v", st.Status, err)
	}
}

// TestReadPathCredentialDerivesReaderFromClaims prova que o principal SELADO no WORM (D6) é o
// `sub` VERIFICADO da credencial, não um header — a atribuição de QUEM leu vem das claims.
func TestReadPathCredentialDerivesReaderFromClaims(t *testing.T) {
	idp := newSovTestIDP(t)
	node := newSovOIDCNode(t, &countingModel{}, idp)
	svc, h := newAPI(t, node)
	submitAndWait(t, svc, "run-205-seal")

	// Um header X-Aos-Reader a forjar OUTRO principal é ignorado — o selo grava o `sub` da claim.
	tok := idp.mintToken(t, sovReaderSub, govBoard)
	headers := bearerHeaders(tok)
	headers[HeaderReaderPrincipal] = "nhi:impostor"
	rec := getReq(h, "/runs/run-205-seal", headers)
	if rec.Code != http.StatusOK {
		t.Fatalf("credencial valida devia dar 200, veio %d", rec.Code)
	}
	part := readAuditPartition("run-205-seal")
	seal, ok, err := node.WORM.At(context.Background(), part, 1)
	if err != nil || !ok {
		t.Fatalf("At(%s,1): ok=%t err=%v", part, ok, err)
	}
	if seal.Principal.NHIID != sovReaderSub {
		t.Fatalf("o selo devia gravar o `sub` verificado %q (nao o header forjado), veio %q", sovReaderSub, seal.Principal.NHIID)
	}
	if seal.Resource.Region != govRegion {
		t.Fatalf("o selo devia gravar a regiao %q resolvida do board da claim, veio %q", govRegion, seal.Resource.Region)
	}
}

// TestReadPathCredentialInvalidTokenDenied prova que um Bearer INVÁLIDO (assinatura de OUTRA
// chave) é recusado fail-closed — a verificação é real, não uma mera presença de header.
func TestReadPathCredentialInvalidTokenDenied(t *testing.T) {
	idp := newSovTestIDP(t)
	node := newSovOIDCNode(t, &countingModel{}, idp)
	svc, h := newAPI(t, node)
	submitAndWait(t, svc, "run-205-bad")

	// Token assinado por um IdP DIFERENTE (kid conhecido, mas chave/assinatura alheia) ⇒ recusado.
	rogue := newSovTestIDP(t)
	rogueTok := rogue.mintToken(t, sovReaderSub, govBoard) // iss do rogue != issuer configurado
	rec := getReq(h, "/runs/run-205-bad", bearerHeaders(rogueTok))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("token de IdP alheio devia dar 404, veio %d (%s)", rec.Code, rec.Body.String())
	}
}

// TestDSAROperatorRequiresStrongCredential prova que o endpoint DSAR (POST /dsar/erase) passa a
// exigir a CREDENCIAL FORTE quando composta: um pedido com header X-Aos-Board forjado mas SEM
// credencial ⇒ 403; com a credencial VÁLIDA ⇒ 200 (erased). Fecha DEF-207/208.
func TestDSAROperatorRequiresStrongCredential(t *testing.T) {
	idp := newSovTestIDP(t)
	node := newSovOIDCNode(t, &countingModel{}, idp)
	svc, err := NewNodeService(node, WithLeaseClock(svcClock()), WithLeaseTTL(time.Minute))
	if err != nil {
		t.Fatalf("NewNodeService: %v", err)
	}
	h, err := NewAPIHandler(svc, node)
	if err != nil {
		t.Fatalf("NewAPIHandler: %v", err)
	}
	seedPII(t, node, "subject-205")

	// FORJADO: board válido no header, sem credencial ⇒ 403 (nao autorizado).
	forged := postReq(h, "/dsar/erase", dsarRequestWire{RequestID: "d1", SubjectID: "subject-205"},
		map[string]string{HeaderReaderPrincipal: govReader, HeaderReaderBoard: govBoard})
	if forged.Code != http.StatusForbidden {
		t.Fatalf("DSAR com header forjado sem credencial devia dar 403, veio %d", forged.Code)
	}
	if _, ok := node.DSARVault.Key(audit.KeyRefFor("subject-205")); !ok {
		t.Fatalf("um DSAR nao-autenticado NAO devia ter apagado a KEK")
	}

	// VÁLIDO: credencial forte ⇒ 200 erased.
	tok := idp.mintToken(t, sovReaderSub, govBoard)
	okrec := postReq(h, "/dsar/erase", dsarRequestWire{RequestID: "d2", SubjectID: "subject-205"}, bearerHeaders(tok))
	if okrec.Code != http.StatusOK {
		t.Fatalf("DSAR com credencial forte devia dar 200, veio %d (%s)", okrec.Code, okrec.Body.String())
	}
}

// TestSovereignAuthorityProvisionAndRotateAudited prova a FONTE DE AUTORIDADE (AOS-205): o
// provisionamento inicial e cada rotação são SELADOS na cadeia WORM (verificável, sem PII), a
// revisão é monotónica e a rotação MUDA a resolução board→região.
func TestSovereignAuthorityProvisionAndRotateAudited(t *testing.T) {
	worm := audit.NewMemStore()
	ctx := context.Background()
	auth, err := NewSovereignRegionAuthority(ctx, map[string]string{govBoard: govRegion}, worm, sovFixedNow)
	if err != nil {
		t.Fatalf("NewSovereignRegionAuthority: %v", err)
	}
	// Provisionamento inicial: revisão 0 e o board semeado resolve.
	if auth.Revision() != 0 {
		t.Fatalf("provisionamento inicial devia ser revisao 0, veio %d", auth.Revision())
	}
	if r, ok := auth.RegionFor(govBoard); !ok || r != govRegion {
		t.Fatalf("board semeado devia resolver para %q, veio (%q,%t)", govRegion, r, ok)
	}
	if _, ok := auth.RegionFor("board:novo-eu"); ok {
		t.Fatalf("board nao provisionado NAO devia resolver (fail-closed)")
	}

	// O provisionamento inicial selou UM registo e a cadeia verifica.
	head, err := worm.Head(ctx, sovereignAuthorityPartition)
	if err != nil || head != 1 {
		t.Fatalf("provisionamento devia selar 1 registo, head=%d err=%v", head, err)
	}

	// ROTAÇÃO: re-provisiona com um board novo; a revisão sobe e a resolução muda.
	rev, err := auth.Rotate(ctx, map[string]string{govBoard: govRegion, "board:novo-eu": "eu"}, "gov:ops-alice")
	if err != nil {
		t.Fatalf("Rotate: %v", err)
	}
	if rev != 1 || auth.Revision() != 1 {
		t.Fatalf("apos rotacao a revisao devia ser 1, veio rev=%d atual=%d", rev, auth.Revision())
	}
	if r, ok := auth.RegionFor("board:novo-eu"); !ok || r != "eu" {
		t.Fatalf("apos rotacao o board novo devia resolver, veio (%q,%t)", r, ok)
	}

	// A rotação selou um SEGUNDO registo e a cadeia inteira verifica (tamper-evidente).
	head2, err := worm.Head(ctx, sovereignAuthorityPartition)
	if err != nil || head2 != 2 {
		t.Fatalf("rotacao devia selar +1 registo (head=2), veio head=%d err=%v", head2, err)
	}
	if err := audit.Verify(ctx, worm, sovereignAuthorityPartition, 1, head2); err != nil {
		t.Fatalf("cadeia de auditoria da autoridade NAO verifica: %v", err)
	}
	// O selo da rotação carrega os metadados de governação (revisão, actor) e NENHUMA PII.
	rot, ok, err := worm.At(ctx, sovereignAuthorityPartition, 2)
	if err != nil || !ok {
		t.Fatalf("At(rotacao): ok=%t err=%v", ok, err)
	}
	if rot.Capability != capSovereignRotate {
		t.Fatalf("capability do selo de rotacao devia ser %q, veio %q", capSovereignRotate, rot.Capability)
	}
	if rot.Principal.NHIID != "gov:ops-alice" {
		t.Fatalf("o selo devia atribuir a rotacao ao actor, veio %q", rot.Principal.NHIID)
	}
	assertNoPIIInPartition(t, worm, sovereignAuthorityPartition)
}

// TestSovereignAuthorityRequiresWORM prova o fail-closed da construção: sem WORM não há auditoria
// de alterações — logo não é autoridade. Recusa com ErrNilAuthorityWORM.
func TestSovereignAuthorityRequiresWORM(t *testing.T) {
	if _, err := NewSovereignRegionAuthority(context.Background(), map[string]string{govBoard: govRegion}, nil, nil); err != ErrNilAuthorityWORM {
		t.Fatalf("construcao sem WORM devia dar ErrNilAuthorityWORM, veio %v", err)
	}
}

// TestReadPathLegacyHeadersWithoutCredential prova a RETRO-COMPAT: um nó com soberania composta
// MAS sem credencial forte (fora de produção) mantém a via legada por headers — a mudança
// endurece o caminho da credencial, não parte o arranque de referência.
func TestReadPathLegacyHeadersWithoutCredential(t *testing.T) {
	// newGovNode (sovereignty_test.go) compõe a autoridade SEM OIDC ⇒ via legada por headers.
	node := newGovNode(t, &countingModel{})
	// A autoridade foi composta (AOS-205), mas sem credencial forte.
	if node.SovereignAuthority == nil {
		t.Fatalf("a autoridade board->regiao devia ter sido composta")
	}
	if node.SovereignReadCredential != nil {
		t.Fatalf("sem OIDC configurado NAO devia haver credencial forte composta")
	}
	svc, h := newAPI(t, node)
	submitAndWait(t, svc, "run-legado-hdr")
	rec := getReq(h, "/runs/run-legado-hdr", govHeaders())
	if rec.Code != http.StatusOK {
		t.Fatalf("via legada por headers devia dar 200 com headers validos, veio %d", rec.Code)
	}
}

// compile-time: a autoridade satisfaz o resolver board→região partilhado com [govsov.Registry].
var _ boardRegionResolver = (*SovereignRegionAuthority)(nil)
var _ boardRegionResolver = (*govsov.Registry)(nil)
