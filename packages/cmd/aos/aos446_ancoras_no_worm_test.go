package main

// aos446_ancoras_no_worm_test.go — o registo das âncoras no arranque, a época v5 e o pino FIDO2
// visto do NÓ (AOS-446 fase 1).

import (
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"errors"
	"strings"
	"testing"
	"time"

	audit "github.com/aos-ref/platform/audit"
	identity "github.com/aos-ref/platform/identity"
)

// pinoDeSoftwareNoUm / pinosDeSoftwareNo embrulham pubkeys ed25519 em pinos, para os testes
// anteriores ao AOS-446 continuarem a montar a Config como montavam.
func pinoDeSoftwareNoUm(t *testing.T, pub ed25519.PublicKey) identity.MandateSigner {
	t.Helper()
	s, err := identity.RawMandateSigner(pub)
	if err != nil {
		t.Fatalf("pino de software: %v", err)
	}
	return s
}

func pinosDeSoftwareNo(m map[string]ed25519.PublicKey) map[string][]identity.MandateSigner {
	out := make(map[string][]identity.MandateSigner, len(m))
	for k, v := range m {
		if s, err := identity.RawMandateSigner(v); err == nil {
			out[k] = []identity.MandateSigner{s}
		}
	}
	return out
}

func chaveAos446(t *testing.T, n byte) ed25519.PublicKey {
	t.Helper()
	seed := make([]byte, ed25519.SeedSize)
	seed[0] = n
	return ed25519.NewKeyFromSeed(seed).Public().(ed25519.PublicKey)
}

// configComAncoras monta uma Config com âncoras em todos os eixos que o registo cobre.
func configComAncoras(t *testing.T) Config {
	t.Helper()
	return Config{
		IssuerPubKey:         chaveAos446(t, 1),
		MandatedIssuerPubKey: chaveAos446(t, 2),
		MandateSigners:       pinosDeSoftwareNo(map[string]ed25519.PublicKey{"alice": chaveAos446(t, 3)}),
		Operators:            map[string]ed25519.PublicKey{"op-1": chaveAos446(t, 4)},
		Ratifiers:            []RatifierConfig{{Principal: "rat-1", PubKey: chaveAos446(t, 5)}},
		Approvers:            []ApproverConfig{{Principal: "ap-1", PubKey: chaveAos446(t, 6), Authority: []string{"approve:danger"}}},
		PolicyTrustAnchor:    chaveAos446(t, 7),
		WORMAnchor:           &WormAnchor{Public: chaveAos446(t, 8)},
		// As duas janelas FECHADAS (zero) — é o estado normal, e o que faz as mutações do
		// TestAOS446CadaAncoraEntraNoDigest morderem.
	}
}

// TestAOS446RegistoDasAncorasSelaSempreENomeiaOQueMudou — o coração do ticket: um pino trocado
// deixa rasto, e um arranque sem troca também deixa (o argumento S-02).
func TestAOS446RegistoDasAncorasSelaSempreENomeiaOQueMudou(t *testing.T) {
	ctx := context.Background()
	store := audit.NewMemStore()
	cfg := configComAncoras(t)
	agora := time.Unix(1790000000, 0).UTC()

	// (1) PRIMEIRO ARRANQUE: a partição está vazia, e o registo di-lo.
	st1, err := provisionTrustAnchors(ctx, store, cfg, agora)
	if err != nil {
		t.Fatalf("primeiro arranque: %v", err)
	}
	if !st1.sealed || !st1.primeiro || st1.transicao {
		t.Fatalf("o primeiro registo tinha de ser selado e declarar-se primeiro: %+v", st1)
	}
	if !strings.Contains(strings.Join(trustAnchorsBanner(st1), ""), "PRIMEIRO REGISTO") {
		t.Fatal("o banner do primeiro arranque tem de dizer que nao ha nada com que comparar")
	}

	// (2) SEGUNDO ARRANQUE, sem trocar nada: sela na mesma (S-02) e declara-se confirmação.
	st2, err := provisionTrustAnchors(ctx, store, cfg, agora.Add(time.Hour))
	if err != nil {
		t.Fatalf("segundo arranque: %v", err)
	}
	if !st2.sealed || st2.transicao || st2.primeiro {
		t.Fatalf("um arranque sem troca tinha de selar uma CONFIRMACAO: %+v", st2)
	}
	if st2.digest != st1.digest {
		t.Fatal("as mesmas ancoras tem de dar o mesmo digest")
	}

	// (3) ROOT TROCA A CHAVE DO HUMANO e reinicia. O registo TEM de o nomear.
	trocada := cfg
	trocada.MandateSigners = pinosDeSoftwareNo(map[string]ed25519.PublicKey{"alice": chaveAos446(t, 99)})
	st3, err := provisionTrustAnchors(ctx, store, trocada, agora.Add(2*time.Hour))
	if err != nil {
		t.Fatalf("terceiro arranque: %v", err)
	}
	if !st3.transicao {
		t.Fatal("trocar AOS_MANDATE_SIGNERS tinha de dar uma TRANSICAO")
	}
	if len(st3.mudancas) != 1 || !strings.HasPrefix(st3.mudancas[0], "mandate_signers:") {
		t.Fatalf("a mudanca tinha de nomear `mandate_signers`, veio %v", st3.mudancas)
	}
	linha := strings.Join(trustAnchorsBanner(st3), "")
	if !strings.Contains(linha, "MUDARAM") || !strings.Contains(linha, "mandate_signers") {
		t.Fatalf("o banner tem de nomear a ancora trocada: %s", linha)
	}

	// (4) OS TRÊS REGISTOS ESTÃO NA CADEIA, e a cadeia verifica.
	head, _ := store.Head(ctx, TrustAnchorsPartition)
	if head != 3 {
		t.Fatalf("esperados 3 registos na particao, ha %d", head)
	}
	if _, err := audit.VerifyStore(ctx, store); err != nil {
		t.Fatalf("a cadeia com os registos das ancoras tinha de verificar: %v", err)
	}
	recs, _ := store.Read(ctx, TrustAnchorsPartition, 1, 3)
	if recs[0].Obligations[0].Type != TrustAnchorsChangedEventType ||
		recs[1].Obligations[0].Type != TrustAnchorsActiveEventType ||
		recs[2].Obligations[0].Type != TrustAnchorsChangedEventType {
		t.Fatalf("os tipos dos registos nao distinguem transicao de confirmacao: %q %q %q",
			recs[0].Obligations[0].Type, recs[1].Obligations[0].Type, recs[2].Obligations[0].Type)
	}
}

// TestAOS446CadaAncoraEntraNoDigest — uma MUTAÇÃO POR EIXO. Sem isto, um digest que só cobrisse
// metade das âncoras passava o teste acima na mesma: a chave do humano é só uma das oito.
func TestAOS446CadaAncoraEntraNoDigest(t *testing.T) {
	base := configComAncoras(t)
	nova := chaveAos446(t, 200)
	casos := map[string]func(*Config){
		"issuer_pubkey":          func(c *Config) { c.IssuerPubKey = nova },
		"mandated_issuer_pubkey": func(c *Config) { c.MandatedIssuerPubKey = nova },
		"mandate_signers":        func(c *Config) { c.MandateSigners = pinosDeSoftwareNo(map[string]ed25519.PublicKey{"alice": nova}) },
		"operators":              func(c *Config) { c.Operators = map[string]ed25519.PublicKey{"op-1": nova} },
		"ratifiers":              func(c *Config) { c.Ratifiers = []RatifierConfig{{Principal: "rat-1", PubKey: nova}} },
		"approvers": func(c *Config) {
			c.Approvers = []ApproverConfig{{Principal: "ap-1", PubKey: nova, Authority: []string{"approve:danger"}}}
		},
		"policy_trust_anchor": func(c *Config) { c.PolicyTrustAnchor = nova },
		"worm_trust_anchor":   func(c *Config) { c.WORMAnchor = &WormAnchor{Public: nova} },
		// A3: AS JANELAS SÃO AUTORIDADE. Sob um v1 o emissor age por qualquer submissor; com dois
		// pinos, duas chaves assinam. Estender qualquer uma das datas alarga o que o nó aceita
		// sem tocar em chave nenhuma — e, antes da revisão adversarial, o digest ficava igual.
		"mandate_v1_until":       func(c *Config) { c.MandateV1Until = time.Unix(1790500000, 0).UTC() },
		"mandate_dual_pin_until": func(c *Config) { c.MandateDualPinUntil = time.Unix(1790500000, 0).UTC() },
	}
	antes := ancorasDaConfig(base)
	for nome, mutar := range casos {
		t.Run(nome, func(t *testing.T) {
			c := base
			mutar(&c)
			depois := ancorasDaConfig(c)
			if depois.Digest() == antes.Digest() {
				t.Fatalf("trocar a ancora %q NAO mudou o digest — essa ancora nao esta coberta", nome)
			}
			diffs := depois.Diferencas(antes)
			if len(diffs) != 1 || !strings.HasPrefix(diffs[0], nome+":") {
				t.Fatalf("a diferenca tinha de nomear SO %q, veio %v", nome, diffs)
			}
		})
	}
	// RENOMEAR um operador (a mesma chave sob outro nome) também é uma troca de âncora.
	renomeado := base
	renomeado.Operators = map[string]ed25519.PublicKey{"op-2": chaveAos446(t, 4)}
	if ancorasDaConfig(renomeado).Digest() == antes.Digest() {
		t.Fatal("mudar o NOME de um operador com a mesma chave tinha de mudar o digest")
	}
	// MUDAR A AUTORIDADE de um aprovador (mesma chave, de safe para danger) também.
	promovido := base
	promovido.Approvers = []ApproverConfig{{Principal: "ap-1", PubKey: chaveAos446(t, 6), Authority: []string{"approve:safe"}}}
	if ancorasDaConfig(promovido).Digest() == antes.Digest() {
		t.Fatal("mudar a autoridade de um aprovador tinha de mudar o digest")
	}
	// ESTENDER uma janela já aberta também muda o digest — não só abri-la de zero.
	aberta := base
	aberta.MandateV1Until = time.Unix(1790400000, 0).UTC()
	esticada := aberta
	esticada.MandateV1Until = time.Unix(1790900000, 0).UTC()
	if ancorasDaConfig(aberta).Digest() == ancorasDaConfig(esticada).Digest() {
		t.Fatal("esticar a janela dos v1 tinha de mudar o digest")
	}
	if ancorasDaConfig(base)["mandate_v1_until"] != ancoraAusente && ancorasDaConfig(base)["mandate_v1_until"] != "(fechada)" {
		t.Fatalf("uma janela fechada sela-se como tal, veio %q", ancorasDaConfig(base)["mandate_v1_until"])
	}
	// UMA ÂNCORA AUSENTE é um facto, e é selada como tal.
	sem := base
	sem.PolicyTrustAnchor = nil
	if ancorasDaConfig(sem)["policy_trust_anchor"] != ancoraAusente {
		t.Fatal("uma ancora ausente tem de ser selada como ausente, nao omitida")
	}
	if ancorasDaConfig(sem).Digest() == antes.Digest() {
		t.Fatal("tirar uma ancora tinha de mudar o digest")
	}
}

// TestAOS446UmPinoFIDO2ELegivelNoRegisto — a impressão que o registo sela é a MESMA que o selo de
// cada decisão usa. Duas fórmulas para a mesma coisa é como se faz um registo que não bate com o
// outro, e é o que este teste impede.
func TestAOS446UmPinoFIDO2ELegivelNoRegisto(t *testing.T) {
	linha := "sk-ssh-ed25519@openssh.com AAAAGnNrLXNzaC1lZDI1NTE5QG9wZW5zc2guY29tAAAAINuZX+JRadFByrm7upK6oB+fLh7OffTLKsBRkPN/zB+dAAAAD3NzaDphb3MtbWFuZGF0ZQ=="
	signers, err := identity.ParseMandateSigners("alice=" + linha)
	if err != nil {
		t.Fatalf("o pino FIDO2 tinha de ser aceite por AOS_MANDATE_SIGNERS: %v", err)
	}
	if len(signers["alice"]) != 1 || !signers["alice"][0].Hardware() {
		t.Fatal("o pino tinha de se declarar hardware")
	}
	cfg := configComAncoras(t)
	cfg.MandateSigners = signers
	cfg.MandatedIssuerID = "iss:aos-issuer-auto"
	if got := ancorasDaConfig(cfg)["mandate_signers"]; got != identity.MandateSignersDigest(signers)[:32] {
		t.Fatalf("o registo tem de usar a impressao canonica do pino, veio %q", got)
	}
	banner := strings.Join(mandatoFIDO2PostureBanner(cfg.MandatedIssuerID, signers), "")
	if !strings.Contains(banner, "HARDWARE") || strings.Contains(banner, "SEED EM FICHEIRO") {
		t.Fatalf("com todos os pinos em hardware o banner nao pode falar de seed em ficheiro: %s", banner)
	}
	// E com um pino de software pelo meio, a postura tem de o DIZER.
	misto := map[string][]identity.MandateSigner{"alice": signers["alice"], "bob": {pinoDeSoftwareNoUm(t, chaveAos446(t, 3))}}
	bannerMisto := strings.Join(mandatoFIDO2PostureBanner(cfg.MandatedIssuerID, misto), "")
	if !strings.Contains(bannerMisto, "SEED EM FICHEIRO") || !strings.Contains(bannerMisto, "1 de 2") {
		t.Fatalf("a postura mista tem de declarar a seed em ficheiro: %s", bannerMisto)
	}
	if mandatoFIDO2PostureBanner("", signers) != nil {
		t.Fatal("sem emissor mandatado nao ha mandatos a assinar, e a linha nao se emite")
	}
}

// TestAOS446EpocaDeEscritaDoWORM — a época sobe por decisão do operador, e as duas variáveis em
// desacordo abortam.
func TestAOS446EpocaDeEscritaDoWORM(t *testing.T) {
	casos := []struct {
		schema, v4 string
		quer       uint8
		erro       bool
	}{
		{"", "", audit.SchemaV3, false},
		{"", "0", audit.SchemaV3, false},
		{"", "1", audit.SchemaV4, false},  // a variavel da onda B1 continua a funcionar
		{"", "on", audit.SchemaV4, false}, //
		{"3", "", audit.SchemaV3, false},  //
		{"4", "", audit.SchemaV4, false},  //
		{"5", "", audit.SchemaV5, false},  //
		{"4", "1", audit.SchemaV4, false}, // concordam
		{"5", "1", 0, true},               // discordam ⇒ aborta
		{"3", "1", 0, true},               // discordam ⇒ aborta
		{"6", "", 0, true},                // epoca que esta release nao escreve
		{"2", "", 0, true},                // epoca que esta release ja nao escreve
		{"cinco", "", 0, true},            //
		{"", "talvez", 0, true},           //
	}
	for _, c := range casos {
		v, err := parseAuditWriteSchema(c.schema, c.v4)
		if c.erro {
			if err == nil {
				t.Errorf("SCHEMA=%q V4=%q tinha de abortar, veio v%d", c.schema, c.v4, v)
			}
			continue
		}
		if err != nil || v != c.quer {
			t.Errorf("SCHEMA=%q V4=%q: esperado v%d, veio v%d (%v)", c.schema, c.v4, c.quer, v, err)
		}
	}
	// O banner declara a época, e o que ela custa.
	v5 := strings.Join(wormV4PostureBanner(audit.SchemaV5, true), "")
	if !strings.Contains(v5, "escreve v5") || !strings.Contains(v5, "mandate_signer") {
		t.Fatalf("o banner do v5 tem de nomear o campo novo: %s", v5)
	}
	v4 := strings.Join(wormV4PostureBanner(audit.SchemaV4, true), "")
	if !strings.Contains(v4, "escreve v4") || !strings.Contains(v4, "NAO o") {
		t.Fatalf("o banner do v4 tem de dizer o que AINDA nao sela: %s", v4)
	}
	v3 := strings.Join(wormV4PostureBanner(0, true), "")
	if !strings.Contains(v3, "escreve v3") || !strings.Contains(v3, "AOS_AUDIT_WRITE_SCHEMA=5") {
		t.Fatalf("o banner por omissao tem de dizer como subir: %s", v3)
	}
	if !strings.Contains(strings.Join(wormV4PostureBanner(audit.SchemaV5, false), ""), "store fornecido pela Config") {
		t.Fatal("com um store injectado o banner nao pode afirmar a epoca que nao controla")
	}
}

// TestAOS446SemWORMNaoArranca — fail-closed, no molde do changelog de política.
func TestAOS446SemWORMNaoArranca(t *testing.T) {
	if _, err := provisionTrustAnchors(context.Background(), nil, configComAncoras(t), time.Now()); err == nil {
		t.Fatal("sem WORM composto o registo das ancoras tinha de recusar")
	}
}

// TestAOS446RegistoDasAncorasEComparavelPeloSelador — o registo tem de ser legível por quem NÃO
// tem a Config: o `aos-issuer worm-seal`, que só vê o ficheiro do WORM. Se a escrita e a leitura
// divergirem, o selador não vê troca nenhuma e o gate fica verde a varrer nada.
func TestAOS446RegistoDasAncorasEComparavelPeloSelador(t *testing.T) {
	ctx := context.Background()
	store := audit.NewMemStore()
	cfg := configComAncoras(t)
	if _, err := provisionTrustAnchors(ctx, store, cfg, time.Unix(1790000000, 0).UTC()); err != nil {
		t.Fatal(err)
	}
	recs, _ := store.Read(ctx, TrustAnchorsPartition, 1, 1)
	lido, digest := trustAnchorsFromParams(recs[0].Obligations[0].Params)
	esperado := ancorasDaConfig(cfg)
	if digest != esperado.Digest() || lido.Digest() != esperado.Digest() {
		t.Fatalf("o que se le do registo nao reproduz o digest selado: %q vs %q", digest, esperado.Digest())
	}
	if len(lido.Diferencas(esperado)) != 0 {
		t.Fatalf("a leitura perdeu ancoras: %v", lido.Diferencas(esperado))
	}
	// E O REGISTO NÃO CONTÉM MATERIAL DE CHAVE — só impressões. Uma pubkey é pública, mas pô-la
	// aqui faria do WORM um directório de chaves que se lê sem as ter, e o registo passaria a
	// crescer com o número de operadores. O que ele precisa de provar é «mudou / não mudou».
	junto := ""
	for _, v := range recs[0].Obligations[0].Params {
		junto += v + "|"
	}
	chaves := []ed25519.PublicKey{cfg.IssuerPubKey, cfg.MandatedIssuerPubKey, cfg.Operators["op-1"],
		cfg.Ratifiers[0].PubKey, cfg.Approvers[0].PubKey, cfg.PolicyTrustAnchor, cfg.WORMAnchor.Public}
	for i, k := range chaves {
		if strings.Contains(junto, hex.EncodeToString(k)) {
			t.Fatalf("a chave %d aparece em claro nos parametros do registo", i)
		}
	}
}

// TestAOS446JanelaDeRotacaoNoArranque — o achado A7 visto da fronteira de ambiente: dois pinos só
// arrancam com a janela ABERTA, e a janela tem tecto.
func TestAOS446JanelaDeRotacaoNoArranque(t *testing.T) {
	agora := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)

	// (1) A VARIÁVEL. Vazia ⇒ fechada; malformada ou para lá do tecto ⇒ aborta.
	if v, err := parseMandateDualPinUntil("", agora); err != nil || !v.IsZero() {
		t.Fatalf("vazia ⇒ janela fechada: %v %v", v, err)
	}
	if v, err := parseMandateDualPinUntil(" 2026-10-10T23:59:59Z ", agora); err != nil || v.IsZero() {
		t.Fatalf("um instante RFC 3339 tinha de ser aceite: %v %v", v, err)
	}
	for _, mau := range []string{"amanha", "2026-10-10", "2099-01-01T00:00:00Z"} {
		if _, err := parseMandateDualPinUntil(mau, agora); !errors.Is(err, ErrBadMandateDualPinUntil) {
			t.Errorf("%q tinha de abortar, veio %v", mau, err)
		}
	}

	// (2) O ARRANQUE. Dois pinos sem janela, ou com a janela já fechada, ABORTAM.
	base := configComAncoras(t)
	base.MandatedIssuerID = issAutoDeTeste
	base.IssuerID = "iss:aos-issuer"
	dois := pinosDeSoftwareNo(map[string]ed25519.PublicKey{"alice": chaveAos446(t, 3)})
	segundo, err := identity.RawMandateSigner(chaveAos446(t, 30))
	if err != nil {
		t.Fatal(err)
	}
	dois["alice"] = append(dois["alice"], segundo)

	semJanela := base
	semJanela.MandateSigners = dois
	if err := validarEmissorMandatado(semJanela); !errors.Is(err, ErrBadMandatedIssuer) {
		t.Fatalf("dois pinos sem janela tinham de abortar, veio %v", err)
	} else if !strings.Contains(err.Error(), "AOS_MANDATE_DUAL_PIN_UNTIL") {
		t.Fatalf("a recusa tem de dizer o que definir: %v", err)
	}
	fechada := semJanela
	fechada.MandateDualPinUntil = time.Now().UTC().Add(-time.Hour)
	if err := validarEmissorMandatado(fechada); !errors.Is(err, ErrBadMandatedIssuer) {
		t.Fatalf("dois pinos com a janela ja fechada tinham de abortar, veio %v", err)
	}
	aberta := semJanela
	aberta.MandateDualPinUntil = time.Now().UTC().Add(time.Hour)
	if err := validarEmissorMandatado(aberta); err != nil {
		t.Fatalf("dois pinos com a janela aberta tinham de arrancar: %v", err)
	}
	// UM pino continua a arrancar com a janela fechada — a janela não é obrigatória.
	um := base
	um.MandateSigners = pinosDeSoftwareNo(map[string]ed25519.PublicKey{"alice": chaveAos446(t, 3)})
	if err := validarEmissorMandatado(um); err != nil {
		t.Fatalf("um pino nao precisa de janela: %v", err)
	}
	// E TRÊS pinos não são uma rotação, mesmo com a janela aberta.
	tres := aberta
	terceiro, err := identity.RawMandateSigner(chaveAos446(t, 31))
	if err != nil {
		t.Fatal(err)
	}
	tres.MandateSigners = map[string][]identity.MandateSigner{"alice": append(append([]identity.MandateSigner(nil), dois["alice"]...), terceiro)}
	if err := validarEmissorMandatado(tres); !errors.Is(err, ErrBadMandatedIssuer) {
		t.Fatalf("tres pinos tinham de abortar mesmo com a janela aberta, veio %v", err)
	}

	// (3) O BANNER diz a verdade nos três estados.
	semLinha := janelaDeRotacaoPostureBanner("", dois, time.Time{}, agora, audit.SchemaV5, time.Time{})
	if semLinha != nil {
		t.Fatal("sem emissor mandatado nao ha linha")
	}
	umPino := strings.Join(janelaDeRotacaoPostureBanner(issAutoDeTeste, um.MandateSigners, time.Time{}, agora, audit.SchemaV5, time.Time{}), "")
	if !strings.Contains(umPino, "UM pino por humano") || !strings.Contains(umPino, "FECHADA") {
		t.Fatalf("banner de um pino: %s", umPino)
	}
	emCurso := strings.Join(janelaDeRotacaoPostureBanner(issAutoDeTeste, dois, agora.Add(time.Hour), agora, audit.SchemaV5, time.Time{}), "")
	if !strings.Contains(emCurso, "EM CURSO para alice") || !strings.Contains(emCurso, "mandate_signer") {
		t.Fatalf("banner da rotacao em curso: %s", emCurso)
	}
	abertaSemRotacao := strings.Join(janelaDeRotacaoPostureBanner(issAutoDeTeste, um.MandateSigners, agora.Add(time.Hour), agora, audit.SchemaV5, time.Time{}), "")
	if !strings.Contains(abertaSemRotacao, "NENHUM humano tem dois pinos") {
		t.Fatalf("banner da janela aberta sem rotacao: %s", abertaSemRotacao)
	}
	// E a postura FIDO2 conta PINOS, não humanos: durante a rotação a chave de software
	// ainda assina, e dizer «1 de 1 humano em hardware» escondia-o.
	misto := map[string][]identity.MandateSigner{"alice": {dois["alice"][0], pinoFIDO2DeProva(t)}}
	linha := strings.Join(mandatoFIDO2PostureBanner(issAutoDeTeste, misto), "")
	if !strings.Contains(linha, "1 de 2 pino(s)") || !strings.Contains(linha, "SEED EM FICHEIRO") {
		t.Fatalf("a postura tem de contar PINOS: %s", linha)
	}
}

// pinoFIDO2DeProva devolve o pino FIDO2 do vector golden.
func pinoFIDO2DeProva(t *testing.T) identity.MandateSigner {
	t.Helper()
	s, err := identity.ParseMandateSigner("sk-ssh-ed25519@openssh.com AAAAGnNrLXNzaC1lZDI1NTE5QG9wZW5zc2guY29tAAAAINuZX+JRadFByrm7upK6oB+fLh7OffTLKsBRkPN/zB+dAAAAD3NzaDphb3MtbWFuZGF0ZQ==")
	if err != nil {
		t.Fatalf("pino FIDO2 de prova: %v", err)
	}
	return s
}

// TestAOS446BannerAvisaQuandoOSeloNaoLevaOPino — O ACHADO B2 DA 2.ª RONDA.
//
// O passo 7 da rotação manda confirmar, no `audit-trail`, QUAL pino verificou o mandato em uso.
// Só que o `mandate_signer` só entra no selo a partir do v5 — abaixo disso o `stampSchema`
// APAGA-O — e produção escreve v3 por omissão. O operador fazia o grep, não via nada, e fechava
// a janela às cegas. O banner passa a dizê-lo, e a consequência gémea: com v3/v4 a guarda da
// retoma compara um campo vazio e não dispara.
func TestAOS446BannerAvisaQuandoOSeloNaoLevaOPino(t *testing.T) {
	agora := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)
	dois := pinosDeSoftwareNo(map[string]ed25519.PublicKey{"alice": chaveAos446(t, 3)})
	segundo, err := identity.RawMandateSigner(chaveAos446(t, 30))
	if err != nil {
		t.Fatal(err)
	}
	dois["alice"] = append(dois["alice"], segundo)
	janela := agora.Add(time.Hour)

	for _, epoca := range []uint8{0, audit.SchemaV3, audit.SchemaV4} {
		linha := strings.Join(janelaDeRotacaoPostureBanner(issAutoDeTeste, dois, janela, agora, epoca, time.Time{}), " ")
		if !strings.Contains(linha, "NAO TEM O QUE LER") {
			t.Fatalf("com a epoca v%d o banner tem de avisar que o passo de confirmacao nao le nada: %s", epoca, linha)
		}
		if !strings.Contains(linha, "AOS_AUDIT_WRITE_SCHEMA=5") {
			t.Fatalf("o aviso tem de dizer o que ligar: %s", linha)
		}
		if !strings.Contains(linha, "guarda da retoma") || !strings.Contains(linha, "VAZIO") {
			t.Fatalf("o aviso tem de declarar a consequencia gemea na retoma (a guarda compara um campo vazio): %s", linha)
		}
	}
	// Com o v5 ligado, o aviso DESAPARECE — senão seria ruído permanente.
	comV5 := strings.Join(janelaDeRotacaoPostureBanner(issAutoDeTeste, dois, janela, agora, audit.SchemaV5, time.Time{}), " ")
	if strings.Contains(comV5, "NAO TEM O QUE LER") {
		t.Fatalf("com o v5 nao ha nada a avisar: %s", comV5)
	}
	// E sem rotação em curso não se emite o aviso: não há passo 7 a executar.
	um := pinosDeSoftwareNo(map[string]ed25519.PublicKey{"alice": chaveAos446(t, 3)})
	semRotacao := strings.Join(janelaDeRotacaoPostureBanner(issAutoDeTeste, um, time.Time{}, agora, audit.SchemaV3, time.Time{}), " ")
	if strings.Contains(semRotacao, "NAO TEM O QUE LER") {
		t.Fatalf("sem rotacao em curso o aviso do v5 nao se aplica: %s", semRotacao)
	}
}

// TestAOS446BannerAvisaAsDuasJanelasAbertas — O ACHADO B4 DA 2.ª RONDA.
//
// Um mandato v1 assinado pelo pino ACABADO DE ACRESCENTAR é aceite, e sob um v1 o emissor age
// por QUALQUER submissor — contorna os `requesters` do AOS-439. Não se recusa (em produção o
// mandato vivo É v1, e recusar partiria a rotação); avisa-se, e o procedimento manda fechar a
// janela dos v1 primeiro.
func TestAOS446BannerAvisaAsDuasJanelasAbertas(t *testing.T) {
	agora := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)
	dois := pinosDeSoftwareNo(map[string]ed25519.PublicKey{"alice": chaveAos446(t, 3)})
	segundo, err := identity.RawMandateSigner(chaveAos446(t, 30))
	if err != nil {
		t.Fatal(err)
	}
	dois["alice"] = append(dois["alice"], segundo)
	janela := agora.Add(time.Hour)

	v1Aberta := strings.Join(janelaDeRotacaoPostureBanner(issAutoDeTeste, dois, janela, agora, audit.SchemaV5, agora.Add(30*24*time.Hour)), " ")
	if !strings.Contains(v1Aberta, "janela dos MANDATOS v1 tambem esta aberta") {
		t.Fatalf("as duas janelas abertas tem de ser avisadas: %s", v1Aberta)
	}
	if !strings.Contains(v1Aberta, "requesters") || !strings.Contains(v1Aberta, "FECHE primeiro") {
		t.Fatalf("o aviso tem de nomear o risco e a ordem: %s", v1Aberta)
	}
	// Com a janela dos v1 FECHADA (zero, ou já passada), não há aviso.
	for _, v1 := range []time.Time{{}, agora.Add(-time.Second)} {
		linha := strings.Join(janelaDeRotacaoPostureBanner(issAutoDeTeste, dois, janela, agora, audit.SchemaV5, v1), " ")
		if strings.Contains(linha, "MANDATOS v1 tambem esta aberta") {
			t.Fatalf("com a janela v1 fechada nao ha aviso: %s", linha)
		}
	}
	// E sem rotação em curso também não: o risco é a SOBREPOSIÇÃO das duas.
	um := pinosDeSoftwareNo(map[string]ed25519.PublicKey{"alice": chaveAos446(t, 3)})
	so := strings.Join(janelaDeRotacaoPostureBanner(issAutoDeTeste, um, time.Time{}, agora, audit.SchemaV5, agora.Add(30*24*time.Hour)), " ")
	if strings.Contains(so, "MANDATOS v1 tambem esta aberta") {
		t.Fatalf("sem rotacao nao ha sobreposicao: %s", so)
	}
}
