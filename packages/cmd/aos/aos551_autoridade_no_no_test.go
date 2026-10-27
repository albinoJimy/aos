package main

import (
	"strings"
	"testing"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	referencemonitor "github.com/aos-ref/kernel/reference-monitor"
	"github.com/aos-ref/kernel/reference-monitor/taint"
)

// AOS-551, fatia 2 — a METADE DO NÓ da autoridade por argumento.
//
// O que esta suite fixa é a conta: `join(piso, untrusted se a call resolveu algum handle)`. O que
// ela NÃO pode fixar, e é por isso que o kernel não depende dela, é que a conta esteja certa: o
// kernel re-junta o piso depois desta reescrita, pelo que um erro aqui só sobe.

func callComPiso(piso taint.Label) referencemonitor.Call {
	return referencemonitor.Call{
		RunID:      "run-551c",
		ToolID:     "web_post",
		Capability: "cap:web_post",
		Context:    referencemonitor.CallContext{Taint: piso.String()},
		Input:      []byte(`{"body":"texto do modelo"}`),
	}
}

// Uma call que NÃO consome handle nenhum não é tocada: o piso que entrou é o que sai. É o limite
// que a fase levanta — num run sem memória nem media o piso é trusted, e a call fica trusted
// mesmo depois de o run ter lido.
func TestAOS551C_No_SemHandleAAutoridadeEOPiso(t *testing.T) {
	for _, piso := range []taint.Label{taint.Trusted, taint.Untrusted} {
		got, err := reescritaDaAutoridade(callComPiso(piso), &bilheteDaCall{})
		if err != nil {
			t.Fatalf("piso %v: %v", piso, err)
		}
		if taint.ParseLabel(got.Context.Taint) != piso {
			t.Errorf("piso %v: a autoridade saiu %q — uma call sem handles não muda de rótulo", piso, got.Context.Taint)
		}
	}
	// E com bilhete nil (uma cadeia sem o elo dos handles) também não muda nada.
	got, err := reescritaDaAutoridade(callComPiso(taint.Trusted), nil)
	if err != nil {
		t.Fatalf("bilhete nil: %v", err)
	}
	if taint.ParseLabel(got.Context.Taint) != taint.Trusted {
		t.Errorf("bilhete nil: a autoridade saiu %q", got.Context.Taint)
	}
}

// Uma call que consome um handle fica UNTRUSTED, qualquer que seja o argumento em que ele entrou.
// Um handle é sempre untrusted — a quarentena nunca promove.
func TestAOS551C_No_ComHandleAAutoridadeSobeSempre(t *testing.T) {
	for _, arg := range []string{"content", "media_type", "qualquer_outro"} {
		b := &bilheteDaCall{}
		b.anotarDigest(arg, "sha256:aa")
		b.anotarResolvido()
		got, err := reescritaDaAutoridade(callComPiso(taint.Trusted), b)
		if err != nil {
			t.Fatalf("arg %s: %v", arg, err)
		}
		if taint.ParseLabel(got.Context.Taint) != taint.Untrusted {
			t.Errorf("arg %s: a call consumiu um handle e saiu %q", arg, got.Context.Taint)
		}
	}
}

// O BLOQUEANTE da primeira versão desta fatia, fechado em teste.
//
// A autoridade perguntava `len(digestPorArgumento) > 0`, e [anotarDigest] deixa cair um argumento
// de NOME VAZIO — um guarda legítimo da linhagem, herdado do AOS-552. Mas o nome dos argumentos é
// escolhido pelo MODELO, e `{"": "<referência>"}` é JSON válido: o handle resolvia-se, os bytes
// entravam na call, o bilhete ficava vazio e a autoridade saía TRUSTED. O Reference Monitor
// autorizava, auditava e selava como trusted uma call cujos argumentos transportavam conteúdo em
// quarentena.
//
// A lição é a FORMA: inferir uma propriedade de uma estrutura filtrada para outro fim herda os
// filtros dela. Este teste fixa a separação das duas perguntas.
func TestAOS551C_No_OArgumentoDeNomeVazioSujaACall(t *testing.T) {
	// O que a resolução faz: conta, e tenta anotar a linhagem (que o descarta pelo nome vazio).
	b := &bilheteDaCall{}
	b.anotarDigest("", "sha256:aa")
	b.anotarResolvido()

	if b.digestDoArgumento("") != "" {
		t.Error("a linhagem passou a aceitar o nome vazio — o guarda do AOS-552 era deliberado")
	}
	if !b.resolveuAlgum() {
		t.Fatal("o bilhete não conta um handle resolvido num argumento de nome vazio")
	}
	got, err := reescritaDaAutoridade(callComPiso(taint.Trusted), b)
	if err != nil {
		t.Fatal(err)
	}
	if taint.ParseLabel(got.Context.Taint) != taint.Untrusted {
		t.Fatalf("a call saiu %q: o RM selaria como TRUSTED uma call com conteúdo em quarentena nos argumentos", got.Context.Taint)
	}

	// E o controlo que prova que a conta não é constante: anotar linhagem SEM resolução (o que
	// nenhum caminho faz) não suja — senão este teste passava com `resolveuAlgum` a devolver
	// sempre true.
	só := &bilheteDaCall{}
	só.anotarDigest("content", "sha256:aa")
	gotSó, err := reescritaDaAutoridade(callComPiso(taint.Trusted), só)
	if err != nil {
		t.Fatal(err)
	}
	if taint.ParseLabel(gotSó.Context.Taint) != taint.Trusted {
		t.Fatal("a autoridade está a ler o mapa da linhagem outra vez")
	}
}

// A reescrita não toca em mais NADA da call. Tocar no efeito é trabalho dos outros elos da
// cadeia; este só decide o rótulo.
func TestAOS551C_No_AReescritaSoTocaNoRotulo(t *testing.T) {
	b := &bilheteDaCall{}
	b.anotarDigest("content", "sha256:aa")
	antes := callComPiso(taint.Trusted)
	depois, err := reescritaDaAutoridade(antes, b)
	if err != nil {
		t.Fatal(err)
	}
	if string(depois.Input) != string(antes.Input) {
		t.Errorf("os argumentos mudaram: %q", depois.Input)
	}
	if depois.ToolID != antes.ToolID || depois.Capability != antes.Capability || depois.RunID != antes.RunID {
		t.Errorf("a identidade da call mudou: %+v", depois)
	}
	if depois.Context.Reversibility != antes.Context.Reversibility {
		t.Errorf("a reversibilidade mudou: %q", depois.Context.Reversibility)
	}
}

// A ORDEM NA CADEIA: a autoridade corre DEPOIS do elo que enche o bilhete.
//
// Sem isso a conta corria sobre um bilhete vazio e toda a call parecia limpa — a forma mais
// silenciosa de este defeito existir, porque nada avermelharia. Prova-se pelo que a ordem compra:
// um elo que anota o bilhete, seguido da autoridade, tem de produzir untrusted; a ordem inversa
// produz o piso.
func TestAOS551C_No_AAutoridadeCorreDepoisDeQuemEncheOBilhete(t *testing.T) {
	anotador := reescritaDaCall(func(call referencemonitor.Call, b *bilheteDaCall) (referencemonitor.Call, error) {
		b.anotarDigest("body", "sha256:aa")
		b.anotarResolvido()
		return call, nil
	})

	// Na ordem certa: o bilhete já está cheio quando a autoridade corre.
	certa := emCadeia(anotador, reescritaDaAutoridade)
	got, err := certa(callComPiso(taint.Trusted))
	if err != nil {
		t.Fatalf("ordem certa: %v", err)
	}
	if taint.ParseLabel(got.Context.Taint) != taint.Untrusted {
		t.Fatalf("ordem certa: a autoridade saiu %q — não viu o handle que o elo anterior resolveu", got.Context.Taint)
	}

	// Na ordem inversa: a autoridade vê um bilhete vazio e a call sai com o piso. É o defeito que
	// a ordem existe para impedir, e é o controlo negativo deste teste.
	inversa := emCadeia(reescritaDaAutoridade, anotador)
	got, err = inversa(callComPiso(taint.Trusted))
	if err != nil {
		t.Fatalf("ordem inversa: %v", err)
	}
	if taint.ParseLabel(got.Context.Taint) != taint.Trusted {
		t.Fatalf("ordem inversa: saiu %q — o controlo negativo não mede nada", got.Context.Taint)
	}
}

// E o elo está composto em [reescritaDoEfeito]: sem plano de handles a cadeia existe e deixa uma
// call limpa passar intacta. Compor a cadeia SEM a autoridade é o que esta asserção impede de
// acontecer em silêncio.
func TestAOS551C_No_AAutoridadeEstaNaCadeiaDoEfeito(t *testing.T) {
	cadeia := reescritaDoEfeito(nil, nil, nil, nil, false, nil)
	if cadeia == nil {
		t.Fatal("a cadeia de reescritas não foi composta")
	}
	got, err := cadeia(callComPiso(taint.Trusted))
	if err != nil {
		t.Fatalf("uma call limpa tinha de passar: %v", err)
	}
	if taint.ParseLabel(got.Context.Taint) != taint.Trusted {
		t.Errorf("uma call sem handles mudou de rótulo: %q", got.Context.Taint)
	}
}

// O banner declara a postura, e declara TAMBÉM que não é ele a garantia — porque o portão passou
// a viver no kernel. Um banner que se apresentasse como a defesa seria o defeito que esta fatia
// corrigiu no desenho.
func TestAOS551C_No_OBannerDeclaraAPosturaEOndeViveAGarantia(t *testing.T) {
	if autoridadeBanner(nil) != nil {
		t.Error("sem plano de handles não pode sair linha nenhuma")
	}
	linhas := autoridadeBanner(&planoDeHandles{})
	if len(linhas) != 1 {
		t.Fatalf("queria 1 linha, veio %d", len(linhas))
	}
	l := linhas[0]
	for _, exige := range []string{
		agentruntime.AssemblyVersion180, // a versão em que isto se aplica
		"ErrArgumentAuthorityRequired",  // o portão do kernel, nomeado
		"RE-JUNTA",                      // a aritmética que garante
		"MEMORIA",                       // o que não se levanta
		"MEDIA",                         // idem, a decisão do dono
		"tool_error",                    // idem
		"AOS-556",                       // quem decide SOBRE o rótulo
	} {
		if !strings.Contains(l, exige) {
			t.Errorf("o banner não declara %q", exige)
		}
	}
	// E diz EXPLICITAMENTE que não é ele a garantia. A primeira versão desta asserção procurava
	// «GARANTIA E ESTA LINHA», string que o banner nunca conteria em versão nenhuma: era vácua.
	if !strings.Contains(l, "A GARANTIA NAO E ESTA LINHA") {
		t.Error("o banner não declara que a garantia vive no kernel e não nele")
	}
}
