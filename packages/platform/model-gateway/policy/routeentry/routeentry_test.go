package routeentry_test

// AOS-519 — O REGISTO DE ENTRADA ASSINADO DE UM PERFIL DE ROTA.
//
// Cada causa de recusa tem aqui o seu caso: parte-se de um registo VÁLIDO e estraga-se UMA coisa.
// O caso exige a causa exacta, pelo que retirar a conferência correspondente faz a pasta carregar
// (ou cair noutra causa) e avermelha esse caso — e só esse. As mutações dirigidas que se fizeram
// estão no Estado do ticket.

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	modelgateway "github.com/aos-ref/platform/model-gateway"
	"github.com/aos-ref/platform/model-gateway/policy/allowlist"
	"github.com/aos-ref/platform/model-gateway/policy/routeentry"
	rt "github.com/aos-ref/platform/model-gateway/policy/routeentry/routeentrytest"
)

// ambiente é uma pasta de registos com a sua chave e a allowlist embebida do gateway.
type ambiente struct {
	dir string
	key rt.Key
	cfg routeentry.Config
}

func novoAmbiente(t *testing.T) *ambiente {
	t.Helper()
	pol, err := allowlist.LoadPolicy()
	if err != nil {
		t.Fatal(err)
	}
	key := rt.NewKey(t)
	dir := t.TempDir()
	return &ambiente{dir: dir, key: key, cfg: routeentry.Config{
		Dir: dir, TrustAnchor: key.Anchor(), Board: rt.Board, Region: rt.Region, Allowlist: pol,
	}}
}

// exigirCausa carrega a pasta e exige a recusa com a causa e o rótulo dados.
func exigirCausa(t *testing.T, cfg routeentry.Config, causa, rotulo string) {
	t.Helper()
	entradas, err := routeentry.LoadDir(cfg)
	if err == nil {
		t.Fatalf("a pasta CARREGOU (%d registos); tinha de ser recusada com %s", len(entradas), causa)
	}
	var r *routeentry.Refusal
	if !errors.As(err, &r) || !errors.Is(err, routeentry.ErrRefused) {
		t.Fatalf("o erro nao e uma recusa: %v", err)
	}
	if r.Cause != causa || r.Label != rotulo {
		t.Fatalf("recusa com causa=%s registo=%q; quer causa=%s registo=%q", r.Cause, r.Label, causa, rotulo)
	}
	if len(entradas) != 0 {
		t.Fatalf("uma recusa devolveu %d registos: nao ha carregamento parcial", len(entradas))
	}
}

// O CAMINHO FELIZ: um registo válido carrega, com os três digests, a fatia e o perfil.
func TestAOS519_RegistoValidoCarrega(t *testing.T) {
	a := novoAmbiente(t)
	entry, report := rt.Good(t, a.dir, "kimi-novo", rt.ProfileJSON, rt.Board, rt.Region, a.key)
	entradas, err := routeentry.LoadDir(a.cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(entradas) != 1 {
		t.Fatalf("registos = %d", len(entradas))
	}
	e := entradas[0]
	perfil, _ := modelgateway.ParseRouteProfile([]byte(rt.ProfileJSON))
	digestDoRelatorio, _ := routeentry.ReportDigest(report)
	if e.Label != "kimi-novo" || e.Profile.Requested != "gpt-4o" || e.Profile.ExpectedModel != "openai/modelo-novo-4.5" ||
		e.ProfileDigest != perfil.Digest() || e.EntryDigest != routeentry.EntryDigest(entry) || e.ReportDigest != digestDoRelatorio ||
		e.ProxyImageDigest != routeentry.ProductionProxyImageDigest() || e.InitialSharePercent != 10 || e.Board != rt.Board || e.Region != rt.Region {
		t.Fatalf("registo carregado: %+v", e)
	}
	// Os três digests têm a forma de um sha256, e são três valores diferentes.
	for _, d := range []string{e.ProfileDigest, e.EntryDigest, e.ReportDigest} {
		if !strings.HasPrefix(d, "sha256:") || len(d) != len("sha256:")+64 {
			t.Fatalf("digest com forma errada: %q", d)
		}
	}
	if e.ProfileDigest == e.EntryDigest || e.EntryDigest == e.ReportDigest || e.ProfileDigest == e.ReportDigest {
		t.Fatal("dois dos tres digests coincidem")
	}
}

// Uma pasta VAZIA carrega zero registos, sem erro — mas só com a âncora.
func TestAOS519_PastaVaziaCarregaZero(t *testing.T) {
	a := novoAmbiente(t)
	entradas, err := routeentry.LoadDir(a.cfg)
	if err != nil || len(entradas) != 0 {
		t.Fatalf("pasta vazia: %d registos, erro %v", len(entradas), err)
	}
}

// Vários registos carregam pela ordem do rótulo, e a ordem dos ficheiros na pasta não conta.
func TestAOS519_VariosRegistosPelaOrdemDoRotulo(t *testing.T) {
	a := novoAmbiente(t)
	mini := strings.Replace(rt.ProfileJSON, `"gpt-4o"`, `"gpt-4o-mini"`, 1)
	rt.Good(t, a.dir, "z-ultimo", rt.ProfileJSON, rt.Board, rt.Region, a.key)
	rt.Good(t, a.dir, "a-primeiro", mini, rt.Board, rt.Region, a.key)
	entradas, err := routeentry.LoadDir(a.cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(entradas) != 2 || entradas[0].Label != "a-primeiro" || entradas[1].Label != "z-ultimo" {
		t.Fatalf("ordem: %+v", entradas)
	}
}

// A ÂNCORA E A PASTA.
func TestAOS519_AncoraEPasta(t *testing.T) {
	t.Run("pasta sem ancora", func(t *testing.T) {
		a := novoAmbiente(t)
		rt.Good(t, a.dir, "r", rt.ProfileJSON, rt.Board, rt.Region, a.key)
		a.cfg.TrustAnchor = "  "
		exigirCausa(t, a.cfg, routeentry.CauseNoTrustAnchor, "")
	})
	t.Run("ancora que nao e uma chave", func(t *testing.T) {
		for _, ma := range []string{"nao-e-base64!", base64.StdEncoding.EncodeToString([]byte("curta")), base64.StdEncoding.EncodeToString(make([]byte, 64))} {
			a := novoAmbiente(t)
			a.cfg.TrustAnchor = ma
			exigirCausa(t, a.cfg, routeentry.CauseBadTrustAnchor, "")
		}
	})
	t.Run("ancora partilhada com a allowlist", func(t *testing.T) {
		a := novoAmbiente(t)
		rt.Good(t, a.dir, "r", rt.ProfileJSON, rt.Board, rt.Region, a.key)
		a.cfg.ForeignAnchors = []string{"lixo", " " + a.key.Anchor() + " "}
		exigirCausa(t, a.cfg, routeentry.CauseSharedTrustAnchor, "")
		// Controlo: com outra âncora alheia, carrega.
		a.cfg.ForeignAnchors = []string{rt.NewKey(t).Anchor()}
		if _, err := routeentry.LoadDir(a.cfg); err != nil {
			t.Fatalf("com uma ancora alheia DIFERENTE tinha de carregar: %v", err)
		}
	})
	t.Run("pasta que nao existe", func(t *testing.T) {
		a := novoAmbiente(t)
		a.cfg.Dir = filepath.Join(a.dir, "nao-existe")
		exigirCausa(t, a.cfg, routeentry.CauseDirUnreadable, "")
	})
	t.Run("pasta que e um ficheiro", func(t *testing.T) {
		a := novoAmbiente(t)
		ficheiro := filepath.Join(a.dir, "x.registo.json")
		if err := os.WriteFile(ficheiro, []byte("{}"), 0o600); err != nil {
			t.Fatal(err)
		}
		a.cfg.Dir = ficheiro
		exigirCausa(t, a.cfg, routeentry.CauseDirUnreadable, "")
	})
	t.Run("ficheiro a mais", func(t *testing.T) {
		for _, nome := range []string{"README.md", "r.registo.json.bak", ".oculto", "com espaco.registo.json", "r.perfil.json"} {
			a := novoAmbiente(t)
			rt.Good(t, a.dir, "r", rt.ProfileJSON, rt.Board, rt.Region, a.key)
			if err := os.WriteFile(filepath.Join(a.dir, nome), []byte("x"), 0o600); err != nil {
				t.Fatal(err)
			}
			exigirCausa(t, a.cfg, routeentry.CauseUnknownFile, "")
		}
	})
	t.Run("subpasta", func(t *testing.T) {
		a := novoAmbiente(t)
		rt.Good(t, a.dir, "r", rt.ProfileJSON, rt.Board, rt.Region, a.key)
		if err := os.Mkdir(filepath.Join(a.dir, "x.registo.json"), 0o700); err != nil {
			t.Fatal(err)
		}
		exigirCausa(t, a.cfg, routeentry.CauseUnknownFile, "x")
	})
	t.Run("assinatura e relatorio sem registo", func(t *testing.T) {
		for _, sufixo := range []string{routeentry.SuffixSignature, routeentry.SuffixReport} {
			a := novoAmbiente(t)
			rt.Good(t, a.dir, "r", rt.ProfileJSON, rt.Board, rt.Region, a.key)
			if err := os.WriteFile(filepath.Join(a.dir, "orfao"+sufixo), []byte("x"), 0o600); err != nil {
				t.Fatal(err)
			}
			exigirCausa(t, a.cfg, routeentry.CauseUnknownFile, "orfao")
		}
	})
	t.Run("demasiados registos", func(t *testing.T) {
		a := novoAmbiente(t)
		for i := 0; i <= routeentry.MaxEntries; i++ {
			// Não precisam de ser válidos: o tecto conta-se antes de se ler seja o que for.
			if err := os.WriteFile(filepath.Join(a.dir, "r"+string(rune('a'+i))+routeentry.SuffixEntry), []byte("{}"), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		exigirCausa(t, a.cfg, routeentry.CauseTooManyEntries, "")
	})
}

// caso é uma recusa de UM registo: parte-se do válido, estraga-se uma coisa, exige-se a causa.
type caso struct {
	nome  string
	causa string
	// perfil substitui o perfil do registo (vazio ⇒ [rt.ProfileJSON]).
	perfil string
	// relatorio muda o relatório antes de selado; registo muda o registo antes de assinado.
	relatorio func(doc map[string]any)
	registo   func(doc map[string]any)
	// depois mexe nos ficheiros já escritos (e na configuração).
	depois func(t *testing.T, a *ambiente, entry, report []byte)
}

func (c caso) correr(t *testing.T) {
	t.Helper()
	a := novoAmbiente(t)
	perfil := c.perfil
	if perfil == "" {
		perfil = rt.ProfileJSON
	}
	rdoc := rt.ReportDoc(rt.ProfileDigest(t, perfil))
	if c.relatorio != nil {
		c.relatorio(rdoc)
	}
	report, reportDigest := rt.SealReport(t, rdoc)
	edoc := rt.EntryDoc(t, perfil, rt.Board, rt.Region, reportDigest)
	if c.registo != nil {
		c.registo(edoc)
	}
	entry := rt.Marshal(t, edoc)
	rt.Write(t, a.dir, "r", entry, routeentry.Sign(entry, a.key.Priv), report)
	if c.depois != nil {
		c.depois(t, a, entry, report)
	}
	exigirCausa(t, a.cfg, c.causa, "r")
}

// escrever reescreve um dos ficheiros do registo `r`.
func escrever(t *testing.T, a *ambiente, sufixo string, conteudo []byte) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(a.dir, "r"+sufixo), conteudo, 0o600); err != nil {
		t.Fatal(err)
	}
}

// reassinar reescreve o registo `r` com outro conteúdo e a assinatura certa para ele.
func reassinar(t *testing.T, a *ambiente, entry []byte) {
	t.Helper()
	escrever(t, a, routeentry.SuffixEntry, entry)
	escrever(t, a, routeentry.SuffixSignature, []byte(routeentry.Sign(entry, a.key.Priv)))
}

// A ASSINATURA.
func TestAOS519_Assinatura(t *testing.T) {
	casos := []caso{
		{nome: "em falta", causa: routeentry.CauseSignatureMissing, depois: func(t *testing.T, a *ambiente, _, _ []byte) {
			if err := os.Remove(filepath.Join(a.dir, "r"+routeentry.SuffixSignature)); err != nil {
				t.Fatal(err)
			}
		}},
		{nome: "de outra chave", causa: routeentry.CauseSignatureInvalid, depois: func(t *testing.T, a *ambiente, entry, _ []byte) {
			escrever(t, a, routeentry.SuffixSignature, []byte(routeentry.Sign(entry, rt.NewKey(t).Priv)))
		}},
		{nome: "ancora de outra chave", causa: routeentry.CauseSignatureInvalid, depois: func(t *testing.T, a *ambiente, _, _ []byte) {
			a.cfg.TrustAnchor = rt.NewKey(t).Anchor()
		}},
		{nome: "registo alterado depois de assinado", causa: routeentry.CauseSignatureInvalid, depois: func(t *testing.T, a *ambiente, entry, _ []byte) {
			escrever(t, a, routeentry.SuffixEntry, []byte(strings.Replace(string(entry), `"fatia_inicial_percentagem": 10`, `"fatia_inicial_percentagem": 90`, 1)))
		}},
		{nome: "um espaco a mais no registo", causa: routeentry.CauseSignatureInvalid, depois: func(t *testing.T, a *ambiente, entry, _ []byte) {
			escrever(t, a, routeentry.SuffixEntry, append(append([]byte(nil), entry...), ' '))
		}},
		{nome: "que nao e base64", causa: routeentry.CauseSignatureInvalid, depois: func(t *testing.T, a *ambiente, _, _ []byte) {
			escrever(t, a, routeentry.SuffixSignature, []byte("isto nao e uma assinatura"))
		}},
		{nome: "vazia", causa: routeentry.CauseSignatureInvalid, depois: func(t *testing.T, a *ambiente, _, _ []byte) {
			escrever(t, a, routeentry.SuffixSignature, nil)
		}},
		// A assinatura é sobre a mensagem COM o prefixo de domínio: uma assinatura dos bytes do
		// registo, ou só do seu digest — o que outra ferramenta de assinar faria com a mesma
		// chave —, não serve.
		{nome: "dos bytes do registo, sem o dominio", causa: routeentry.CauseSignatureInvalid, depois: func(t *testing.T, a *ambiente, entry, _ []byte) {
			escrever(t, a, routeentry.SuffixSignature, []byte(base64.StdEncoding.EncodeToString(ed25519.Sign(a.key.Priv, entry))))
		}},
		{nome: "do digest do registo, sem o dominio", causa: routeentry.CauseSignatureInvalid, depois: func(t *testing.T, a *ambiente, entry, _ []byte) {
			escrever(t, a, routeentry.SuffixSignature, []byte(base64.StdEncoding.EncodeToString(ed25519.Sign(a.key.Priv, []byte(routeentry.EntryDigest(entry))))))
		}},
	}
	for _, c := range casos {
		t.Run(c.nome, c.correr)
	}
}

// CONTROLO NEGATIVO DA ASSINATURA: um registo assinado como se assina a allowlist — com a chave
// da allowlist, sobre o digest, sem o domínio dos perfis — é recusado, e a mesma chave não serve
// de âncora dos perfis quando é a da allowlist. As duas âncoras não são intermutáveis.
func TestAOS519_AChaveDaAllowlistNaoAssinaPerfis(t *testing.T) {
	a := novoAmbiente(t)
	chaveDaAllowlist := rt.NewKey(t)
	entry, _ := rt.Good(t, a.dir, "r", rt.ProfileJSON, rt.Board, rt.Region, a.key)
	// 1. O dono assinou o registo com a chave ERRADA (a da allowlist): a âncora dos perfis não a verifica.
	escrever(t, a, routeentry.SuffixSignature, []byte(routeentry.Sign(entry, chaveDaAllowlist.Priv)))
	exigirCausa(t, a.cfg, routeentry.CauseSignatureInvalid, "r")
	// 2. O operador pôs a chave da allowlist como âncora dos perfis: recusa-se a âncora, mesmo
	//    que a assinatura verificasse contra ela.
	a.cfg.TrustAnchor = chaveDaAllowlist.Anchor()
	a.cfg.ForeignAnchors = []string{chaveDaAllowlist.Anchor()}
	exigirCausa(t, a.cfg, routeentry.CauseSharedTrustAnchor, "")
}

// O REGISTO.
func TestAOS519_Registo(t *testing.T) {
	outroPerfil := strings.Replace(rt.ProfileJSON, "modelo-novo-4.5", "outro-modelo", 1)
	casos := []caso{
		{nome: "campo desconhecido", causa: routeentry.CauseEntryUnreadable, registo: func(d map[string]any) { d["relaxar_allowlist"] = true }},
		{nome: "campo em falta", causa: routeentry.CauseEntryUnreadable, registo: func(d map[string]any) { delete(d, "regiao") }},
		{nome: "campo com outra caixa", causa: routeentry.CauseEntryUnreadable, registo: func(d map[string]any) {
			d["Board"] = d["board"]
			delete(d, "board")
		}},
		{nome: "valor de tipo errado", causa: routeentry.CauseEntryUnreadable, registo: func(d map[string]any) { d["board"] = 7 }},
		{nome: "digest sem a forma de um sha256", causa: routeentry.CauseEntryUnreadable, registo: func(d map[string]any) { d["digest_do_relatorio"] = "sha256:abc" }},
		{nome: "fatia que nao e um inteiro", causa: routeentry.CauseEntryUnreadable, registo: func(d map[string]any) { d["fatia_inicial_percentagem"] = "10" }},
		{nome: "fatia com fraccao", causa: routeentry.CauseEntryUnreadable, registo: func(d map[string]any) { d["fatia_inicial_percentagem"] = 10.5 }},
		{nome: "chave repetida", causa: routeentry.CauseEntryUnreadable, depois: func(t *testing.T, a *ambiente, entry, _ []byte) {
			reassinar(t, a, []byte(strings.Replace(string(entry), `"board":`, `"fatia_inicial_percentagem": 100, "board":`, 1)))
		}},
		{nome: "conteudo depois do registo", causa: routeentry.CauseEntryUnreadable, depois: func(t *testing.T, a *ambiente, entry, _ []byte) {
			reassinar(t, a, append(append([]byte(nil), entry...), []byte(`{"formato":"x"}`)...))
		}},
		{nome: "nao e JSON", causa: routeentry.CauseEntryUnreadable, depois: func(t *testing.T, a *ambiente, _, _ []byte) {
			reassinar(t, a, []byte("perfil: gpt-4o"))
		}},
		{nome: "versao de formato desconhecida", causa: routeentry.CauseUnknownFormat, registo: func(d map[string]any) { d["formato"] = "aos-registo-de-entrada-de-perfil/2" }},
		// Um formato futuro traz membros que este leitor não conhece: diz «formato», não «ilegível».
		{nome: "formato futuro com campos novos", causa: routeentry.CauseUnknownFormat, registo: func(d map[string]any) {
			d["formato"], d["titular"] = "aos-registo-de-entrada-de-perfil/2", true
		}},
		{nome: "fatia acima de 100", causa: routeentry.CauseShareOutOfRange, registo: func(d map[string]any) { d["fatia_inicial_percentagem"] = 101 }},
		{nome: "fatia negativa", causa: routeentry.CauseShareOutOfRange, registo: func(d map[string]any) { d["fatia_inicial_percentagem"] = -1 }},
		{nome: "perfil com campo desconhecido", causa: routeentry.CauseBadProfile, registo: func(d map[string]any) {
			d["perfil"] = json.RawMessage(strings.Replace(rt.ProfileJSON, `{"requested"`, `{"temperature":1,"requested"`, 1))
		}},
		// A chave-sombra (revisão): `requeſted`, com U+017F, que o encoding/json casa com `requested`.
		// O registo é assinado e o digest declarado é o do perfil «à vista»: sem a leitura fechada
		// por conjunto de nomes, o nó carregava o perfil com o nome da sombra.
		{nome: "perfil com chave-sombra por fold", causa: routeentry.CauseBadProfile, registo: func(d map[string]any) {
			d["perfil"] = json.RawMessage(strings.Replace(rt.ProfileJSON, `"capabilities":["tools"]`, `"capabilities":["tools"],"requeſted":"gpt-4o-mini"`, 1))
		}},
		{nome: "perfil com wire desconhecido", causa: routeentry.CauseBadProfile, registo: func(d map[string]any) {
			d["perfil"] = json.RawMessage(strings.Replace(rt.ProfileJSON, "openai-chat-completions", "outro-wire", 1))
		}},
		{nome: "nome pedido com ponto", causa: routeentry.CauseBadRouteName, perfil: strings.Replace(rt.ProfileJSON, `"gpt-4o"`, `"gpt-4.1"`, 1)},
		{nome: "nome pedido com barra", causa: routeentry.CauseBadRouteName, perfil: strings.Replace(rt.ProfileJSON, `"gpt-4o"`, `"openai/gpt-4o"`, 1)},
		{nome: "digest do perfil que nao e o do perfil", causa: routeentry.CauseProfileDigestMismatch, registo: func(d map[string]any) {
			d["digest_do_perfil"] = "sha256:" + strings.Repeat("0", 64)
		}},
		// O perfil foi trocado por outro, mantendo o digest (e o veredicto) do que foi qualificado.
		{nome: "perfil trocado sob o mesmo digest", causa: routeentry.CauseProfileDigestMismatch, registo: func(d map[string]any) {
			d["perfil"] = json.RawMessage(outroPerfil)
		}},
		{nome: "devolver opcional", causa: routeentry.CauseProfileReturnsState,
			perfil: strings.Replace(rt.ProfileJSON, `"capabilities":["tools"]`, `"capabilities":["tools"],"projection_version":"1.3.0","devolver":"opcional"`, 1)},
		{nome: "devolver obrigatorio", causa: routeentry.CauseProfileReturnsState,
			perfil: strings.Replace(rt.ProfileJSON, `"capabilities":["tools"]`, `"capabilities":["tools"],"projection_version":"1.3.0","devolver":"obrigatorio","tool_call_id":"provider","devolver_em":"topo"`, 1)},
		{nome: "de outro board", causa: routeentry.CauseOtherEnvironment, registo: func(d map[string]any) { d["board"] = "board-us" }},
		{nome: "de outra regiao", causa: routeentry.CauseOtherEnvironment, registo: func(d map[string]any) { d["regiao"] = "eu-west" }},
		{nome: "imagem do proxy que nao e a declarada", causa: routeentry.CauseProxyImageNotDeclared, registo: func(d map[string]any) {
			d["digest_da_imagem_do_proxy"] = "sha256:" + strings.Repeat("a", 64)
		}},
	}
	for _, c := range casos {
		t.Run(c.nome, c.correr)
	}
}

// Um perfil que escreve `devolver: nunca`, `tool_call_id: runtime` e `devolver_em: origem` NÃO
// devolve estado: é a omissão por extenso, e carrega.
func TestAOS519_AOmissaoPorExtensoCarrega(t *testing.T) {
	a := novoAmbiente(t)
	porExtenso := strings.Replace(rt.ProfileJSON, `"capabilities":["tools"]`, `"capabilities":["tools"],"devolver":"nunca","tool_call_id":"runtime","devolver_em":"origem"`, 1)
	rt.Good(t, a.dir, "r", porExtenso, rt.Board, rt.Region, a.key)
	entradas, err := routeentry.LoadDir(a.cfg)
	if err != nil || len(entradas) != 1 || routeentry.ReturnsState(entradas[0].Profile) {
		t.Fatalf("a omissao por extenso tinha de carregar: %v", err)
	}
	// E o digest é o do perfil sem esses campos: a omissão não muda o digest.
	if entradas[0].ProfileDigest != rt.ProfileDigest(t, rt.ProfileJSON) {
		t.Fatal("a omissao por extenso mudou o digest do perfil")
	}
}

// O RELATÓRIO CITADO.
func TestAOS519_Relatorio(t *testing.T) {
	casos := []caso{
		{nome: "em falta", causa: routeentry.CauseReportMissing, depois: func(t *testing.T, a *ambiente, _, _ []byte) {
			if err := os.Remove(filepath.Join(a.dir, "r"+routeentry.SuffixReport)); err != nil {
				t.Fatal(err)
			}
		}},
		{nome: "vazio", causa: routeentry.CauseReportUnreadable, depois: func(t *testing.T, a *ambiente, _, _ []byte) {
			escrever(t, a, routeentry.SuffixReport, nil)
		}},
		{nome: "alterado sem refazer o digest", causa: routeentry.CauseReportInvalid, depois: func(t *testing.T, a *ambiente, _, report []byte) {
			escrever(t, a, routeentry.SuffixReport, []byte(strings.Replace(string(report), `"terminou": "completa"`, `"terminou": "interrompida"`, 1)))
		}},
		{nome: "sem o campo do digest", causa: routeentry.CauseReportInvalid, depois: func(t *testing.T, a *ambiente, _, _ []byte) {
			escrever(t, a, routeentry.SuffixReport, []byte(`{"versao_do_relatorio":"1"}`))
		}},
		// Um relatório ÍNTEGRO (confere o seu próprio digest) que não é o que o dono assinou: é o
		// caso que o digest sozinho não apanha, e a razão de o registo o citar.
		{nome: "outro relatorio, integro", causa: routeentry.CauseReportNotCited, depois: func(t *testing.T, a *ambiente, _, _ []byte) {
			doc := rt.ReportDoc(rt.ProfileDigest(t, rt.ProfileJSON))
			doc["data_utc"] = "2026-10-12T00:00:00Z"
			outro, _ := rt.SealReport(t, doc)
			escrever(t, a, routeentry.SuffixReport, outro)
		}},
		{nome: "versao desconhecida", causa: routeentry.CauseReportUnknownShape, relatorio: func(d map[string]any) { d["versao_do_relatorio"] = "2" }},
		{nome: "membro desconhecido no topo", causa: routeentry.CauseReportUnknownShape, relatorio: func(d map[string]any) { d["veredicto_revisto"] = "qualificado" }},
		{nome: "membro desconhecido na qualificacao", causa: routeentry.CauseReportUnknownShape, relatorio: func(d map[string]any) { rt.Qualification(d)["veredicto_final"] = "qualificado" }},
		{nome: "membro desconhecido na rota qualificada", causa: routeentry.CauseReportUnknownShape, relatorio: func(d map[string]any) { rt.QualifiedRoute(d)["digest_do_perfil_real"] = "x" }},
		{nome: "veredicto que nao e um texto", causa: routeentry.CauseReportUnknownShape, relatorio: func(d map[string]any) { rt.Qualification(d)["veredicto"] = true }},
		{nome: "vale_para_producao que nao e um booleano", causa: routeentry.CauseReportUnknownShape, relatorio: func(d map[string]any) { rt.Qualification(d)["vale_para_producao"] = "true" }},
		{nome: "sem vale_para_producao", causa: routeentry.CauseReportUnknownShape, relatorio: func(d map[string]any) { delete(rt.Qualification(d), "vale_para_producao") }},
		{nome: "sem modo", causa: routeentry.CauseReportUnknownShape, relatorio: func(d map[string]any) { delete(d, "modo") }},
		{nome: "sem o bloco da qualificacao", causa: routeentry.CauseReportWithoutVerdict, relatorio: func(d map[string]any) { delete(d, "qualificacao_do_modelo") }},
		{nome: "de outra experiencia", causa: routeentry.CauseReportWithoutVerdict, relatorio: func(d map[string]any) { d["experiencia"] = "bateria" }},
		{nome: "modo falso com vale_para_producao false", causa: routeentry.CauseVerdictNotForProduction, relatorio: func(d map[string]any) {
			d["modo"] = "falso"
			rt.Qualification(d)["vale_para_producao"] = false
			rt.Qualification(d)["porque_nao_vale_para_producao"] = "o_fornecedor_e_um_provider_falso_e_o_veredicto_nao_vale_para_producao"
		}},
		// Cada uma das duas metades da conferência, sozinha.
		{nome: "modo proxy que diz que vale", causa: routeentry.CauseVerdictNotForProduction, relatorio: func(d map[string]any) { d["modo"] = "proxy" }},
		{nome: "modo real que diz que nao vale", causa: routeentry.CauseVerdictNotForProduction, relatorio: func(d map[string]any) { rt.Qualification(d)["vale_para_producao"] = false }},
		{nome: "recusado", causa: routeentry.CauseNotQualified, relatorio: func(d map[string]any) {
			rt.Qualification(d)["veredicto"], rt.Qualification(d)["razoes"] = "recusado", []string{"nos_com_tools_nao_cumpridos"}
		}},
		{nome: "inconclusivo", causa: routeentry.CauseNotQualified, relatorio: func(d map[string]any) {
			rt.Qualification(d)["veredicto"], rt.Qualification(d)["razoes"] = "inconclusivo", []string{"amostra_incompleta"}
		}},
		{nome: "veredicto fora do vocabulario", causa: routeentry.CauseNotQualified, relatorio: func(d map[string]any) { rt.Qualification(d)["veredicto"] = "Qualificado" }},
		{nome: "de outro perfil", causa: routeentry.CauseVerdictOfOtherProfile, relatorio: func(d map[string]any) {
			rt.QualifiedRoute(d)["digest_do_perfil"] = "sha256:" + strings.Repeat("b", 64)
		}},
		{nome: "de outra imagem do proxy", causa: routeentry.CauseVerdictOfOtherProxyImage, relatorio: func(d map[string]any) {
			rt.QualifiedRoute(d)["digest_da_imagem_do_proxy"] = "sha256:" + strings.Repeat("c", 64)
		}},
		{nome: "sem imagem do proxy", causa: routeentry.CauseVerdictOfOtherProxyImage, relatorio: func(d map[string]any) {
			delete(rt.QualifiedRoute(d), "digest_da_imagem_do_proxy")
		}},
	}
	for _, c := range casos {
		t.Run(c.nome, c.correr)
	}
}

// O RELATÓRIO FORJADO COM CHAVES EM CAIXA DIFERENTE (a revisão do AOS-518). Um relatório do modo
// `falso`, com veredicto `recusado`, a que se acrescentam `"MODO":"real"` e um segundo bloco
// `Qualificacao_do_modelo` com `qualificado`: um `json.Unmarshal` do Go lia os valores forjados.
// Aqui o registo foi ASSINADO a citar esse relatório (o pior caso: o dono enganou-se), e mesmo
// assim o nó não o lê como qualificado.
func TestAOS519_RelatorioForjadoComChavesEmCaixaDiferente(t *testing.T) {
	a := novoAmbiente(t)
	doc := rt.ReportDoc(rt.ProfileDigest(t, rt.ProfileJSON))
	doc["modo"] = "falso"
	rt.Qualification(doc)["veredicto"] = "recusado"
	rt.Qualification(doc)["vale_para_producao"] = false

	// Os membros forjados vão no FIM do documento: de dois nomes que casam, o leitor do Go fica
	// com o último.
	forjado := rt.ReportDoc(rt.ProfileDigest(t, rt.ProfileJSON))
	honesto := strings.TrimRight(string(rt.Marshal(t, doc)), "} \n")
	semDigest := honesto + `, "MODO": "real", "Qualificacao_do_modelo": ` + string(rt.Marshal(t, rt.Qualification(forjado))) + `}`
	// O que um leitor que casa nomes por caixa leria deste documento: `real` e `qualificado`.
	var lido struct {
		Modo                 string `json:"modo"`
		QualificacaoDoModelo struct {
			Veredicto        string `json:"veredicto"`
			ValeParaProducao bool   `json:"vale_para_producao"`
		} `json:"qualificacao_do_modelo"`
	}
	if err := json.Unmarshal([]byte(semDigest), &lido); err != nil {
		t.Fatal(err)
	}
	if lido.Modo != "real" || lido.QualificacaoDoModelo.Veredicto != "qualificado" || !lido.QualificacaoDoModelo.ValeParaProducao {
		t.Fatalf("o controlo nao reproduz o defeito: json.Unmarshal leu %+v", lido)
	}
	// O nó recusa-o: a forma canónica não aceita nomes repetidos sem distinguir a caixa.
	if _, err := routeentry.ReadVerdict([]byte(semDigest)); routeentry.CauseOf(err) != routeentry.CauseReportInvalid {
		t.Fatalf("o relatorio forjado tinha de ser recusado como invalido; veio %v", err)
	}
	// E um relatório SÓ com as chaves em maiúsculas (sem as minúsculas a repetir) também não é
	// lido como qualificado: a leitura não casa `MODO` com `modo`.
	soMaiusculas := map[string]any{}
	for k, v := range rt.ReportDoc(rt.ProfileDigest(t, rt.ProfileJSON)) {
		soMaiusculas[k] = v
	}
	soMaiusculas["MODO"] = soMaiusculas["modo"]
	delete(soMaiusculas, "modo")
	cru, digest := rt.SealReport(t, soMaiusculas)
	if _, err := routeentry.ReadVerdict(cru); routeentry.CauseOf(err) != routeentry.CauseReportUnknownShape {
		t.Fatalf("um relatorio com MODO em vez de modo tinha de ser recusado pela forma; veio %v", err)
	}
	// Ponta a ponta: assinado a citar esse relatório, a pasta é recusada.
	entry := rt.Marshal(t, rt.EntryDoc(t, rt.ProfileJSON, rt.Board, rt.Region, digest))
	rt.Write(t, a.dir, "r", entry, routeentry.Sign(entry, a.key.Priv), cru)
	exigirCausa(t, a.cfg, routeentry.CauseReportUnknownShape, "r")
}

// A ALLOWLIST CONTINUA A MANDAR, e dois registos não disputam o mesmo nome.
func TestAOS519_AllowlistENomeRepetido(t *testing.T) {
	t.Run("nome que a allowlist nao autoriza", func(t *testing.T) {
		caso{causa: routeentry.CauseNameNotAllowlisted, perfil: strings.Replace(rt.ProfileJSON, `"gpt-4o"`, `"claude-sonnet"`, 1)}.correr(t)
	})
	t.Run("nome autorizado noutra regiao", func(t *testing.T) {
		// O registo é para a região do nó, e o nó está numa região que a allowlist não cobre.
		a := novoAmbiente(t)
		rt.Good(t, a.dir, "r", rt.ProfileJSON, rt.Board, "us-east", a.key)
		a.cfg.Region = "us-east"
		exigirCausa(t, a.cfg, routeentry.CauseNameNotAllowlisted, "r")
	})
	t.Run("sem allowlist", func(t *testing.T) {
		a := novoAmbiente(t)
		rt.Good(t, a.dir, "r", rt.ProfileJSON, rt.Board, rt.Region, a.key)
		a.cfg.Allowlist = nil
		exigirCausa(t, a.cfg, routeentry.CauseNameNotAllowlisted, "r")
	})
	t.Run("dois registos para o mesmo nome pedido", func(t *testing.T) {
		a := novoAmbiente(t)
		rt.Good(t, a.dir, "a", rt.ProfileJSON, rt.Board, rt.Region, a.key)
		rt.Good(t, a.dir, "b", strings.Replace(rt.ProfileJSON, "modelo-novo-4.5", "outro-modelo", 1), rt.Board, rt.Region, a.key)
		exigirCausa(t, a.cfg, routeentry.CauseDuplicateName, "b")
	})
}

// UM REGISTO MAU RECUSA A PASTA TODA: não há «carrega o que conseguir».
func TestAOS519_UmRegistoMauRecusaAPastaToda(t *testing.T) {
	a := novoAmbiente(t)
	rt.Good(t, a.dir, "a-bom", strings.Replace(rt.ProfileJSON, `"gpt-4o"`, `"gpt-4o-mini"`, 1), rt.Board, rt.Region, a.key)
	entry, _ := rt.Good(t, a.dir, "b-mau", rt.ProfileJSON, rt.Board, rt.Region, a.key)
	escrever2 := filepath.Join(a.dir, "b-mau"+routeentry.SuffixSignature)
	if err := os.WriteFile(escrever2, []byte(routeentry.Sign(entry, rt.NewKey(t).Priv)), 0o600); err != nil {
		t.Fatal(err)
	}
	exigirCausa(t, a.cfg, routeentry.CauseSignatureInvalid, "b-mau")
}

// A MENSAGEM DE UMA RECUSA só leva a causa e o rótulo: nenhum conteúdo dos ficheiros.
func TestAOS519_ARecusaNaoRepeteConteudo(t *testing.T) {
	a := novoAmbiente(t)
	segredo := "SENTINELA-CONTEUDO-DO-REGISTO"
	doc := rt.EntryDoc(t, rt.ProfileJSON, rt.Board, rt.Region, "sha256:"+strings.Repeat("0", 64))
	doc["board"] = segredo
	entry := rt.Marshal(t, doc)
	rt.Write(t, a.dir, "r", entry, routeentry.Sign(entry, a.key.Priv), []byte(`{"x":"`+segredo+`"}`))
	_, err := routeentry.LoadDir(a.cfg)
	if err == nil || strings.Contains(err.Error(), segredo) {
		t.Fatalf("a recusa repete conteudo do registo: %v", err)
	}
	if quer := "routeentry: registo de entrada de perfil de rota recusado: causa=registo_de_outro_ambiente registo=r"; err.Error() != quer {
		t.Fatalf("mensagem = %q; quer %q", err.Error(), quer)
	}
}

// O VOCABULÁRIO DAS CAUSAS é fechado, sem repetidos, e cada causa é exercitada por um caso acima
// (a lista é conferida com as constantes; quem acrescentar uma causa acrescenta-a aos dois sítios).
func TestAOS519_VocabularioDasCausas(t *testing.T) {
	causas := routeentry.Causes()
	if len(causas) != 33 {
		t.Fatalf("causas = %d; o vocabulario mudou — reveja o runbook e o ADR", len(causas))
	}
	vistas := map[string]bool{}
	for _, c := range causas {
		if vistas[c] || c == "" || strings.ContainsAny(c, " \t\n\"") || c != strings.ToLower(c) {
			t.Errorf("causa mal formada ou repetida: %q", c)
		}
		vistas[c] = true
	}
	if routeentry.CauseOf(errors.New("outro erro")) != "" {
		t.Error("um erro que nao e uma recusa nao tem causa")
	}
}

// A IMAGEM DO PROXY DECLARADA tem a forma de uma referência fixada por digest.
func TestAOS519_ImagemDoProxyDeclarada(t *testing.T) {
	d := routeentry.ProductionProxyImageDigest()
	if !strings.HasPrefix(d, "sha256:") || len(d) != len("sha256:")+64 || !strings.HasSuffix(routeentry.ProductionProxyImage, "@"+d) {
		t.Fatalf("digest da imagem do proxy = %q", d)
	}
}

// Os nomes dos membros que o nó lê do relatório são os que as listas fechadas admitem.
func TestAOS519_ListasFechadasCobremOQueSeLe(t *testing.T) {
	tem := func(lista []string, nome string) bool {
		for _, n := range lista {
			if n == nome {
				return true
			}
		}
		return false
	}
	doc := rt.ReportDoc("sha256:" + strings.Repeat("0", 64))
	for nome := range doc {
		if !tem(routeentry.ReportTopFields(), nome) {
			t.Errorf("topo: %q fora da lista", nome)
		}
	}
	for nome := range rt.Qualification(doc) {
		if !tem(routeentry.QualificationFields(), nome) {
			t.Errorf("qualificacao: %q fora da lista", nome)
		}
	}
	for nome := range rt.QualifiedRoute(doc) {
		if !tem(routeentry.QualifiedRouteFields(), nome) {
			t.Errorf("rota qualificada: %q fora da lista", nome)
		}
	}
	if !reflect.DeepEqual(routeentry.ReportTopFields(), routeentry.ReportTopFields()) {
		t.Error("as listas nao sao estaveis")
	}
}
