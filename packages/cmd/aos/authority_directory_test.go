package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	referencemonitor "github.com/aos-ref/kernel/reference-monitor"
)

// ---------------------------------------------------------------------------
// AOS-071 — o directório de autoridade externo sai de DORMENTE
// ---------------------------------------------------------------------------
//
// Sem directório, o ScopeGate acaba a verificar `capability ∈ token.Scope` — que o hook de
// identidade já impôs. A segunda opinião independente existia no gate mas nenhum
// deployment a conseguia provisionar: [Config.Authority] só era atribuível por código.
// Consequência prática: NÃO havia revogação — um token válido valia até expirar,
// acontecesse o que acontecesse à organização.
//
// Estes testes selam as quatro propriedades que tornam o directório seguro de ligar.

// acToken devolve a autoridade DERIVADA DO TOKEN para os três sujeitos que o ScopeGate
// dobra: a raiz humana, o agente e o eixo CLASSE ("agent:<classe>"), que é intersectado à
// parte em Evaluate. Omitir a classe faz a dobra colapsar e nega tudo por outra razão — o
// que tornaria estes testes vacuosos.
func acToken(caps ...string) map[string][]string {
	return map[string][]string{
		"human:alice":        caps,
		"agt-1":              caps,
		"agent:agent-worker": caps,
	}
}

func escreveDirectorio(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "authority.json")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatalf("escrever directorio: %v", err)
	}
	return p
}

// TestDirectorioAutoridade_RestringeMasNuncaAmplia é a propriedade estrutural: o directório
// intersecta com o grant ASSINADO do token. Pode tirar; não pode dar. É por isso que o
// ficheiro não precisa de ser assinado como o bundle de política.
func TestDirectorioAutoridade_RestringeMasNuncaAmplia(t *testing.T) {
	dir, err := parseAuthorityFile(escreveDirectorio(t, `{"revision":3,"subjects":[
	  {"subject":"agt-1","capabilities":["cap:fs.read","cap:http.post","cap:inventada"]},
	  {"subject":"agent:agent-worker","capabilities":["cap:fs.read","cap:http.post"]},
	  {"subject":"human:alice","capabilities":["cap:fs.read","cap:http.post"]}
	]}`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	gate := referencemonitor.NewScopeGate(dir)

	// O token concede fs.read a ambos os sujeitos; o directório dá MAIS ao agente
	// (http.post, e até uma capability inventada) e MENOS ao humano.
	call := &referencemonitor.Call{
		Capability: "cap:http.post",
		Principal: referencemonitor.Principal{
			NHIID: "agt-1", AgentClass: "agent-worker",
			DelegationChain:  []referencemonitor.DelegationHop{{Sub: "human:alice", ActAs: "agt-1"}},
			SubjectAuthority: acToken("cap:fs.read"),
		},
	}
	res, err := gate.Evaluate(context.Background(), call)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if res.Decision != referencemonitor.HookDeny {
		t.Fatalf("o directorio NAO pode conceder o que o token nao assinou; veio %v", res.Decision)
	}
	// CONTROLO POSITIVO: a capability que o token ASSINA passa. Sem isto, o deny acima
	// poderia vir de qualquer outra coisa e o teste não provaria nada.
	call.Capability = "cap:fs.read"
	call.Principal.Authority = nil
	ok, err := gate.Evaluate(context.Background(), call)
	if err != nil {
		t.Fatalf("Evaluate (controlo): %v", err)
	}
	if ok.Decision == referencemonitor.HookDeny {
		t.Fatalf("a capability assinada pelo token e reconhecida pelo directorio devia passar; veio deny (%s)", ok.Reason)
	}
}

// TestDirectorioAutoridade_RevogaComListaVazia é a capacidade que não existia: retirar a
// autoridade a um sujeito SEM esperar que o token expire.
func TestDirectorioAutoridade_RevogaComListaVazia(t *testing.T) {
	ctx := context.Background()
	token := acToken("cap:fs.read")
	chain := []referencemonitor.DelegationHop{{Sub: "human:alice", ActAs: "agt-1"}}
	novaCall := func() *referencemonitor.Call {
		return &referencemonitor.Call{
			Capability: "cap:fs.read",
			Principal: referencemonitor.Principal{
				NHIID: "agt-1", AgentClass: "agent-worker",
				DelegationChain: chain, SubjectAuthority: token,
			},
		}
	}

	// ANTES: o directório reconhece a capability — passa.
	antes, err := parseAuthorityFile(escreveDirectorio(t, `{"subjects":[{"subject":"agt-1","capabilities":["cap:fs.read"]},{"subject":"agent:agent-worker","capabilities":["cap:fs.read"]}]}`))
	if err != nil {
		t.Fatalf("parse antes: %v", err)
	}
	if res, _ := referencemonitor.NewScopeGate(antes).Evaluate(ctx, novaCall()); res.Decision == referencemonitor.HookDeny {
		t.Fatalf("pre-condicao: com a capability no directorio a call devia passar; veio deny (%s)", res.Reason)
	}

	// DEPOIS: a MESMA capability, o MESMO token — e o directório revoga o sujeito.
	depois, err := parseAuthorityFile(escreveDirectorio(t, `{"revision":2,"subjects":[{"subject":"agt-1","capabilities":[]}]}`))
	if err != nil {
		t.Fatalf("parse depois: %v", err)
	}
	if depois.Revoked != 1 {
		t.Fatalf("uma entrada com capabilities vazias conta como REVOGACAO; revogados=%d", depois.Revoked)
	}
	res, err := referencemonitor.NewScopeGate(depois).Evaluate(ctx, novaCall())
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if res.Decision != referencemonitor.HookDeny {
		t.Fatal("um sujeito REVOGADO no directorio tem de ser negado, mesmo com o token ainda valido")
	}
}

// TestDirectorioAutoridade_SujeitoAusenteNaoEhRestringido é a propriedade que torna seguro
// ligar um directório PARCIAL — e a que mais engana: remover uma entrada NÃO revoga, devolve
// o sujeito à autoridade plena do seu token. É por isso que o banner e a doc o dizem em voz
// alta, e é por isso que a revogação se faz com lista vazia e não apagando a linha.
func TestDirectorioAutoridade_SujeitoAusenteNaoEhRestringido(t *testing.T) {
	// O directório só conhece OUTRO agente; nada diz sobre agt-1 nem sobre a alice.
	dir, err := parseAuthorityFile(escreveDirectorio(t, `{"subjects":[{"subject":"agt-outro","capabilities":[]}]}`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	res, err := referencemonitor.NewScopeGate(dir).Evaluate(context.Background(), &referencemonitor.Call{
		Capability: "cap:fs.read",
		Principal: referencemonitor.Principal{
			NHIID: "agt-1", AgentClass: "agent-worker",
			DelegationChain:  []referencemonitor.DelegationHop{{Sub: "human:alice", ActAs: "agt-1"}},
			SubjectAuthority: acToken("cap:fs.read"),
		},
	})
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if res.Decision == referencemonitor.HookDeny {
		t.Fatalf("um sujeito AUSENTE do directorio cai no token e NAO e restringido; veio deny (%s)", res.Reason)
	}
}

// TestDirectorioAutoridade_ParseFailClosed: um directório que não se consegue ler não pode
// degradar para "sem restrição" — é exactamente aí que uma revogação deixaria de ser
// aplicada e quem o configurou julgaria estar protegido.
func TestDirectorioAutoridade_ParseFailClosed(t *testing.T) {
	casos := []struct{ nome, body string }{
		{"json invalido", `{"subjects":`},
		{"campo desconhecido", `{"subjects":[{"subject":"a","capabilities":[],"extra":1}]}`},
		{"sem sujeitos", `{"subjects":[]}`},
		{"sujeito vazio", `{"subjects":[{"subject":"   ","capabilities":["cap:fs.read"]}]}`},
		{"sujeito duplicado", `{"subjects":[{"subject":"agt-1","capabilities":["cap:fs.read"]},{"subject":"agt-1","capabilities":[]}]}`},
	}
	for _, c := range casos {
		t.Run(c.nome, func(t *testing.T) {
			_, err := parseAuthorityFile(escreveDirectorio(t, c.body))
			if !errors.Is(err, ErrBadAuthorityFile) {
				t.Fatalf("devia abortar com ErrBadAuthorityFile; veio %v", err)
			}
		})
	}
	// Ficheiro inexistente também aborta (um caminho mal escrito no compose não pode
	// resultar num nó sem revogação que ninguém nota).
	if _, err := parseAuthorityFile("/caminho/que/nao/existe.json"); !errors.Is(err, ErrBadAuthorityFile) {
		t.Fatalf("ficheiro inexistente devia abortar; veio %v", err)
	}
}

// TestDirectorioAutoridade_NaoConfiguradoEhInalterado: sem a variável, nada muda — o
// directório é opt-in e o comportamento anterior é byte-idêntico.
func TestDirectorioAutoridade_NaoConfiguradoEhInalterado(t *testing.T) {
	dir, err := parseAuthorityFile("")
	if err != nil || dir != nil {
		t.Fatalf("nao configurado ⇒ (nil,nil); veio (%v,%v)", dir, err)
	}
	dir, err = parseAuthorityFile("   ")
	if err != nil || dir != nil {
		t.Fatalf("so espacos ⇒ (nil,nil); veio (%v,%v)", dir, err)
	}
}

// TestDirectorioAutoridade_FingerprintEstavelEIndependenteDaOrdem: o fingerprint serve para
// uma ROTAÇÃO ser visível nos logs. Tem de mudar com o conteúdo e NÃO com a ordem das
// linhas, senão reordenar o ficheiro pareceria uma rotação e uma rotação real poderia
// passar despercebida entre ruído.
func TestDirectorioAutoridade_FingerprintEstavelEIndependenteDaOrdem(t *testing.T) {
	a, err := parseAuthorityFile(escreveDirectorio(t, `{"subjects":[
	  {"subject":"agt-1","capabilities":["cap:fs.read","cap:http.post"]},
	  {"subject":"agt-2","capabilities":["cap:fs.read"]}]}`))
	if err != nil {
		t.Fatalf("parse a: %v", err)
	}
	b, err := parseAuthorityFile(escreveDirectorio(t, `{"subjects":[
	  {"subject":"agt-2","capabilities":["cap:fs.read"]},
	  {"subject":"agt-1","capabilities":["cap:http.post","cap:fs.read"]}]}`))
	if err != nil {
		t.Fatalf("parse b: %v", err)
	}
	if a.Fingerprint != b.Fingerprint {
		t.Fatalf("reordenar nao e rotacao: %s vs %s", a.Fingerprint, b.Fingerprint)
	}
	c, err := parseAuthorityFile(escreveDirectorio(t, `{"subjects":[
	  {"subject":"agt-1","capabilities":["cap:fs.read"]},
	  {"subject":"agt-2","capabilities":["cap:fs.read"]}]}`))
	if err != nil {
		t.Fatalf("parse c: %v", err)
	}
	if a.Fingerprint == c.Fingerprint {
		t.Fatal("tirar uma capability E uma rotacao: o fingerprint tinha de mudar")
	}
	if len(a.Fingerprint) == 0 || strings.ContainsAny(a.Fingerprint, " \t") {
		t.Fatalf("fingerprint mal formado: %q", a.Fingerprint)
	}
}
