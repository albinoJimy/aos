package modelgateway_test

// AOS-519 (revisão adversarial) — A LEITURA FECHADA DO PERFIL NÃO CASA NOMES POR «FOLD».
//
// O `encoding/json` casa `requeſted` (com U+017F, o s longo) com o campo `requested`, e `K`
// (U+212A, o sinal de Kelvin) com `k`. A guarda de antes exigia «minúsculas e sem repetidos», e um
// perfil com `requested` e `requeſted` lia-se com o valor do SEGUNDO, sem erro. Agora o nome de
// cada membro, em todos os níveis, tem de ser byte a byte um dos nomes conhecidos desse nível.

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	modelgateway "github.com/aos-ref/platform/model-gateway"
)

// Os digests dos perfis de exemplo versionados do banco, medidos ANTES da guarda nova.
const (
	aos519DigestDoExemploDevolucao  = "sha256:b27665e779dc8cbc70339a7e66d016bdabaccd11da549ef3637435e71117769f"
	aos519DigestDoExemploOpenRouter = "sha256:02efe3ae2db78a09f7c37ce3965bea7f7157ffb5724744e7a5a9db8ff894e38c"
)

const (
	aos519SLongo = "ſ" // ſ: o fold do encoding/json casa-o com `s`
	aos519Kelvin = "K" // K: o fold do encoding/json casa-o com `k`
)

func TestAOS519_OPerfilNaoCasaNomesPorFold(t *testing.T) {
	base := `"requested":"gpt-4o","expected_model":"openai/k3","wire_class":"openai-chat-completions","capabilities":["tools"]`
	casos := map[string]string{
		// TOPO, com ſ: a chave-sombra da sonda do revisor.
		"topo: requeſted a sombra de requested": `{` + base + `,"reque` + aos519SLongo + `ted":"gpt-4o-mini"}`,
		"topo: so requeſted":                    `{"reque` + aos519SLongo + `ted":"gpt-4o","expected_model":"openai/k3","wire_class":"openai-chat-completions","capabilities":["tools"]}`,
		"topo: paramſ":                          `{` + base + `,"param` + aos519SLongo + `":{"max_tokens":4096}}`,
		"topo: capabilitieſ":                    `{` + base + `,"capabilitie` + aos519SLongo + `":[]}`,
		// (Nenhum nome do topo tem `k`: o Kelvin exercita-se nos níveis de baixo.)
		// PARAMS, com ſ e com K.
		"params: max_tokenſ":                   `{` + base + `,"params":{"max_token` + aos519SLongo + `":4096}}`,
		"params: max_toKens (Kelvin)":          `{` + base + `,"params":{"max_to` + aos519Kelvin + `ens":4096}}`,
		"params: max_tokens e a sombra Kelvin": `{` + base + `,"params":{"max_tokens":16000,"max_to` + aos519Kelvin + `ens":1}}`,
		"params: reaſoning_effort":             `{` + base + `,"params":{"rea` + aos519SLongo + `oning_effort":"high"}}`,
		"params: thinKing (Kelvin)":            `{` + base + `,"params":{"thin` + aos519Kelvin + `ing":{"type":"disabled"}}}`,
		// THINKING, com ſ e com K.
		"thinking: budget_toKens (Kelvin)": `{` + base + `,"params":{"thinking":{"type":"enabled","budget_to` + aos519Kelvin + `ens":1024}}}`,
		"thinking: budget_tokenſ":          `{` + base + `,"params":{"thinking":{"type":"enabled","budget_token` + aos519SLongo + `":1024}}}`,
		// REASONING, com ſ e com K.
		"reasoning: max_toKens (Kelvin)": `{` + base + `,"params":{"reasoning":{"max_to` + aos519Kelvin + `ens":1024}}}`,
		"reasoning: max_tokenſ":          `{` + base + `,"params":{"reasoning":{"max_token` + aos519SLongo + `":1024}}}`,
		// Um objecto onde o perfil não tem nenhum.
		"objecto dentro de capabilities": `{"requested":"gpt-4o","expected_model":"openai/k3","wire_class":"openai-chat-completions","capabilities":[{"requested":"x"}]}`,
		"objecto no lugar de um texto":   `{"requested":{"requested":"gpt-4o"},"expected_model":"openai/k3","wire_class":"openai-chat-completions","capabilities":["tools"]}`,
		// As formas de antes continuam recusadas.
		"maiusculas":     `{"Requested":"gpt-4o","expected_model":"openai/k3","wire_class":"openai-chat-completions","capabilities":["tools"]}`,
		"chave repetida": `{` + base + `,"requested":"gpt-4o-mini"}`,
	}
	for nome, doc := range casos {
		if p, err := modelgateway.ParseRouteProfile([]byte(doc)); !errors.Is(err, modelgateway.ErrBadRouteProfile) {
			t.Errorf("%s: tinha de ser recusado; leu requested=%q params=%+v (err=%v)", nome, p.Requested, p.Params, err)
		}
	}
	// CONTROLO: é mesmo o descodificador que casa estes nomes — sem a guarda, a sombra ganhava.
	var cru struct {
		Requested string `json:"requested"`
	}
	if err := json.Unmarshal([]byte(casos["topo: requeſted a sombra de requested"]), &cru); err != nil || cru.Requested != "gpt-4o-mini" {
		t.Fatalf("o controlo nao reproduz o defeito: json.Unmarshal leu %q (%v)", cru.Requested, err)
	}
	var comK struct {
		MaxTokens int `json:"max_tokens"`
	}
	if err := json.Unmarshal([]byte(`{"max_tokens":16000,"max_to`+aos519Kelvin+`ens":1}`), &comK); err != nil || comK.MaxTokens != 1 {
		t.Fatalf("o controlo do Kelvin nao reproduz o defeito: leu %d (%v)", comK.MaxTokens, err)
	}
	// O erro não repete o conteúdo.
	_, err := modelgateway.ParseRouteProfile([]byte(casos["topo: requeſted a sombra de requested"]))
	if strings.Contains(err.Error(), "gpt-4o-mini") || strings.Contains(err.Error(), aos519SLongo) {
		t.Fatalf("o erro repete o conteudo do perfil: %v", err)
	}
}

// A GUARDA NOVA NÃO MUDA O QUE JÁ SE LIA. Os perfis com todos os campos de todos os níveis
// continuam a ler-se, e os digests são os de antes: os da tabela (os goldens do AOS-513) e os dos
// perfis de exemplo versionados do banco de ensaio.
func TestAOS519_OsPerfisDeAntesLeemSeEOsDigestsNaoMudam(t *testing.T) {
	for _, doc := range []string{
		`{"requested":"gpt-4o","expected_model":"openai/k3","wire_class":"openai-chat-completions","capabilities":["tools"]}`,
		`{"requested":"gpt-4o","expected_model":"openai/k3","wire_class":"openai-chat-completions","capabilities":["tools"],"params":{"thinking":{"type":"enabled","budget_tokens":1024},"max_tokens":16000},"projection_version":"1.3.0","devolver":"obrigatorio","tool_call_id":"provider","devolver_em":"topo"}`,
		`{"requested":"gpt-4o","expected_model":"openai/k3","wire_class":"openai-chat-completions","capabilities":[],"params":{"reasoning":{"effort":"high"}},"devolver":"nunca","tool_call_id":"runtime","devolver_em":"origem"}`,
	} {
		if _, err := modelgateway.ParseRouteProfile([]byte(doc)); err != nil {
			t.Errorf("um perfil valido deixou de se ler: %v\n%s", err, doc)
		}
	}
	// Os digests da tabela em código, relidos do JSON de cada perfil pela leitura fechada.
	for _, p := range modelgateway.RouteProfiles() {
		cru, _ := json.Marshal(p)
		lido, err := modelgateway.ParseRouteProfile(cru)
		if err != nil {
			t.Fatalf("o perfil %q da tabela nao se le do seu proprio JSON: %v", p.Requested, err)
		}
		if lido.Digest() != aos513Digests[p.Requested] {
			t.Errorf("perfil %q: digest %s; o de antes e %s", p.Requested, lido.Digest(), aos513Digests[p.Requested])
		}
	}
	// Os perfis de exemplo versionados do banco de ensaio: lêem-se, e os digests são estes.
	exemplos := map[string]string{
		"claude-devolucao.exemplo.json":  aos519DigestDoExemploDevolucao,
		"claude-openrouter.exemplo.json": aos519DigestDoExemploOpenRouter,
	}
	for ficheiro, quer := range exemplos {
		cru, err := os.ReadFile(filepath.Join("..", "..", "qa", "banco-ensaio", "perfis", ficheiro))
		if err != nil {
			t.Fatal(err)
		}
		p, err := modelgateway.ParseRouteProfile(cru)
		if err != nil {
			t.Fatalf("%s deixou de se ler: %v", ficheiro, err)
		}
		if p.Digest() != quer {
			t.Errorf("%s: digest %s; medido na base, antes da guarda nova: %s", ficheiro, p.Digest(), quer)
		}
	}
}
