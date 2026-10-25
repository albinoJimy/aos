package pdp

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"os"
	"path/filepath"
	"testing"
)

// A POLÍTICA 1.2.0 CANDIDATA, PROVADA SEM A CHAVE DO DONO (AOS-556, ADR-044).
//
// O texto vive em `docs/governance/politica-1.2.0-candidata.cedar` e NÃO está em vigor: pô-lo no
// bundle sem assinatura nova faz o `content_hash` recomputado deixar de bater com o manifesto, o
// PDP recusa fail-closed, e nenhum nó arranca com política. O que este teste faz é assiná-lo numa
// pasta TEMPORÁRIA com uma chave descartável — o mesmo mecanismo que vinte e tantos testes deste
// pacote já usam — e provar as decisões que ele toma.
//
// Isto não é um substituto da cerimónia: é o que impede a cerimónia de ser o primeiro momento em
// que alguém descobre que a regra não decide o que o ADR diz que decide. A chave do dono continua
// a ser a única coisa que põe a política em vigor.

const candidataRel = "../../../docs/governance/politica-1.2.0-candidata.cedar"

// abrirCandidata assina a política candidata numa pasta temporária, com a allowlist de referência,
// e devolve o PDP carregado dela.
func abrirCandidata(t testing.TB) *PDP {
	t.Helper()
	texto, err := os.ReadFile(candidataRel)
	if err != nil {
		t.Fatalf("a politica candidata nao se le em %s: %v", candidataRel, err)
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "aos_authz.cedar"), texto, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, capabilitiesDir), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, capabilitiesDir, "allowlist.json"), refAllowlist(t), 0o644); err != nil {
		t.Fatal(err)
	}
	_, priv := newKeypair(t)
	if _, err := SignBundle(dir, "1.2.0", priv); err != nil {
		t.Fatalf("SignBundle da candidata: %v", err)
	}
	pub := priv.Public().(ed25519.PublicKey)
	if err := os.WriteFile(filepath.Join(dir, trustAnchorFile),
		[]byte(base64.StdEncoding.EncodeToString(pub)+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	p, err := Open(dir)
	if err != nil {
		t.Fatalf("Open da candidata: %v", err)
	}
	return p
}

// fsReadCandidata é o Input base de uma leitura: authority com a capability, região `eu`, e os dois
// campos novos por preencher (é cada caso que os põe).
func fsReadCandidata() Input {
	in := fsRead()
	in.Context.Destination = ""
	in.Context.Provenance = ""
	return in
}

func TestAOS556_PoliticaCandidataDecidePorDestinoEProveniencia(t *testing.T) {
	t.Parallel()
	p := abrirCandidata(t)
	ctx := context.Background()

	if v := p.ActiveVersion(); v != "1.2.0" {
		t.Fatalf("ActiveVersion()=%q; a candidata e a 1.2.0", v)
	}

	for nome, c := range map[string]struct {
		muda  func(*Input)
		quero Effect
	}{
		// --- allow_fs_read_local: par allow+deny -------------------------------------------
		//
		// A AFIRMAÇÃO QUE IMPORTA: uma leitura local passa COM O CONTEXTO UNTRUSTED. É a emenda
		// de 2026-10-19 ao ADR-034, que até aqui vivia só na classificação estrutural do
		// TaintGate e passa a estar na política assinada.
		"leitura local, contexto untrusted": {
			func(in *Input) {
				in.Context.Destination = "local_read"
				in.Context.Taint = "untrusted"
				in.Context.Provenance = "context"
			}, Permit},
		"leitura local, contexto limpo": {
			func(in *Input) { in.Context.Destination = "local_read" }, Permit},
		// E O APERTO DELIBERADO: um eixo implícito no manifesto dá `unknown`, e `unknown` não
		// satisfaz regra nenhuma. A declaração dos três eixos passa a ser pré-condição da
		// permissão, e não uma boa prática.
		"destino unknown, contexto limpo": {
			func(in *Input) { in.Context.Destination = "unknown" }, Deny},
		"destino vazio, contexto limpo": {
			func(in *Input) {}, Deny},

		// --- allow_fs_read_external: a REGRA DE FLUXO, com o par e o controlo negativo -------
		//
		// É ESTA LINHA QUE COMPRA A FASE R2: a call consome um handle untrusted e é PERMITIDA,
		// porque o destino não muda nada no mundo e o que a suja é o que ela própria consome.
		"leitura externa, untrusted por handle": {
			func(in *Input) {
				in.Context.Destination = "external_read"
				in.Context.Taint = "untrusted"
				in.Context.Provenance = "handle_argument"
			}, Permit},
		"leitura externa, contexto limpo": {
			func(in *Input) { in.Context.Destination = "external_read" }, Permit},
		// O CONTROLO NEGATIVO DO DONO: o mesmo plano COM MEDIA continua negado. Num run com media
		// o piso já é untrusted, logo a proveniência é `context` — a disjunção falha nos dois
		// lados. A política não sabe o que é media: decide pela etiqueta.
		"leitura externa, untrusted pelo contexto": {
			func(in *Input) {
				in.Context.Destination = "external_read"
				in.Context.Taint = "untrusted"
				in.Context.Provenance = "context"
			}, Deny},
		// E a proveniência não se auto-concede: sem authority não há permit, qualquer que ela seja.
		"leitura externa por handle, sem authority": {
			func(in *Input) {
				in.Principal.Authority = nil
				in.Context.Destination = "external_read"
				in.Context.Taint = "untrusted"
				in.Context.Provenance = "handle_argument"
			}, Deny},
		// Um destino de EFEITO nunca é leitura, mesmo com a capability de leitura.
		"destino de efeito com cap de leitura": {
			func(in *Input) {
				in.Context.Destination = "external_effect"
				in.Context.Taint = "untrusted"
				in.Context.Provenance = "handle_argument"
			}, Deny},
	} {
		in := fsReadCandidata()
		c.muda(&in)
		dec, err := p.Decide(ctx, in)
		if err != nil {
			t.Errorf("%s: Decide: %v", nome, err)
			continue
		}
		if dec.Effect != c.quero {
			t.Errorf("%s: effect=%q; quero %q (razao: %s)", nome, dec.Effect, c.quero, dec.Reason)
		}
	}
}

// A ASSIMETRIA, que é o critério central da fase: um handle untrusted NUNCA flui para um efeito.
// A `allow_http_post` fica INALTERADA e nenhuma proveniência satisfaz a sua cláusula de taint —
// não há excepção a escrever, e é por isso que esta é a metade simples da decisão.
func TestAOS556_PoliticaCandidataNegaOEfeitoSobreUmHandleUntrusted(t *testing.T) {
	t.Parallel()
	p := abrirCandidata(t)
	ctx := context.Background()

	for nome, c := range map[string]struct {
		muda  func(*Input)
		quero Effect
	}{
		"post com handle untrusted": {
			func(in *Input) {
				in.Context.Taint = "untrusted"
				in.Context.Provenance = "handle_argument"
				in.Context.Destination = "external_effect"
			}, Deny},
		// Nem sequer declarando o destino de uma leitura externa: a regra do post não lê o
		// destino, e a da leitura externa não cobre esta capability.
		"post a fingir-se leitura externa": {
			func(in *Input) {
				in.Context.Taint = "untrusted"
				in.Context.Provenance = "handle_argument"
				in.Context.Destination = "external_read"
			}, Deny},
		// NÃO-VACUIDADE: com o contexto limpo o post continua a ser permitido, como na 1.1.0.
		// Sem este caso, os dois denies acima passariam com uma política que negasse tudo.
		"post com contexto limpo": {
			func(in *Input) { in.Context.Provenance = "clean" }, Permit},
	} {
		in := httpPost()
		c.muda(&in)
		dec, err := p.Decide(ctx, in)
		if err != nil {
			t.Errorf("%s: Decide: %v", nome, err)
			continue
		}
		if dec.Effect != c.quero {
			t.Errorf("%s: effect=%q; quero %q (razao: %s)", nome, dec.Effect, c.quero, dec.Reason)
		}
	}
}

// A CANDIDATA NÃO PODE USAR UM ATRIBUTO FORA DO MAPA DO MOTOR. Uma regra que o faça COMPILA e
// ASSINA, e em runtime devolve erro de avaliação — que contamina toda a autorização daquela
// capability, um deny-all silencioso. É o defeito que o `policy-sign` corre o smoke para apanhar;
// aqui corre-se sobre a candidata, antes da cerimónia.
func TestAOS556_PoliticaCandidataPassaOSmokePorRegra(t *testing.T) {
	t.Parallel()
	p := abrirCandidata(t)
	if err := p.SmokeDecideRules(context.Background()); err != nil {
		t.Fatalf("a candidata nao passa o smoke por regra: %v", err)
	}
	// NÃO-VACUIDADE: as quatro regras esperadas estão na policy set. Sem isto, um bundle que
	// carregasse zero regras passaria o smoke por vacuidade.
	ids := map[string]bool{}
	for _, id := range p.RuleIDs() {
		ids[id] = true
	}
	for _, q := range []string{"allow_http_post", "allow_fs_read_local", "allow_fs_read_external", "allow_artifact_write"} {
		if !ids[q] {
			t.Errorf("a candidata nao tem a regra %q (tem: %v)", q, p.RuleIDs())
		}
	}
	if ids["allow_fs_read"] {
		t.Error("a candidata ainda tem a allow_fs_read inteira: a decisao do dono foi PARTI-LA por destino")
	}
}
