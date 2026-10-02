package plannerprompt

// AOS-400 — o template declara o schema que `plan.Decode` exige.
//
// O template 1.1.0 pedia «um PlanDocument JSON de schema FECHADO» sem dizer quais eram os
// campos, e o modelo de produção falhou as três tentativas com `plan: objective de topo em
// falta`. Os testes daqui prendem o texto do prompt ao contrato em código:
//   - os NOMES dos campos saem das tags JSON dos tipos de `plan`;
//   - a OBRIGATORIEDADE sai do próprio decode, por tipo (retira-se o campo de um documento
//     válido e vê-se se o decode recusa);
//   - cada campo procura-se na LINHA onde o template o declara: os do topo na secção do
//     topo, os do nó na secção do nó, os aninhados nas chavetas da linha do campo-pai. Um
//     `objective*` no nó não tapa a ausência do `objective*` do topo, e um `name*` de
//     `tools` não tapa um `name` sem `*` em `outputs`.
//
// Limites declarados: a obrigatoriedade dos campos do predicado depende do subject e fica
// fixa no teste (subject e op obrigatórios); os valores fechados são uma lista escrita à mão
// a partir das constantes de `plan` — uma constante nova não é detectada sozinha.

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/aos-ref/control-plane/orchestrator/plan"
	"github.com/aos-ref/control-plane/orchestrator/planvalidate"
)

// documentoCompleto usa todos os campos dos tipos cuja obrigatoriedade se mede por remoção
// (na linha 1.2.0, para caberem outputs e consumes). É válido para o DECODE, que é o que se
// mede aqui; não para o validador (analise liga-se a recolha pelos dois canais).
const documentoCompleto = `{
  "plan_version": "1.2.0",
  "objective": "recolher e analisar",
  "budget_total": {"tokens": 100, "cost_micro_usd": 100},
  "planner_meta": {"model": "m", "prompt_version": "1.2.0", "capabilities_hash": "sha256:x"},
  "nodes": [
    {"node_id": "recolha", "role": "worker", "objective": "recolher",
     "tools": [{"name": "fs.read", "version": "1.0.0", "digest": "sha256:aaa"}],
     "depends_on": [],
     "budget_estimate": {"tokens": 10, "cost_micro_usd": 10},
     "risk_class": "safe",
     "outputs": [{"name": "dados", "type": "record", "taint": "untrusted"}]},
    {"node_id": "analise", "role": "worker", "objective": "analisar",
     "tools": [], "depends_on": ["recolha"],
     "budget_estimate": {"tokens": 10, "cost_micro_usd": 10},
     "conditional_on": [{"from": "recolha", "when": [{"subject": "terminal_state", "op": "eq", "enum": "complete"}]}],
     "consumes": [{"from": "recolha", "output": "dados", "type": "record"}]}
  ]
}`

// Cabeçalhos do bloco SCHEMA.
const (
	cabecalhoTopo  = "Topo do documento:"
	cabecalhoNo    = "Cada no de nodes:"
	cabecalhoPred  = "Predicado de when:"
	cabecalhoForma = "FORMA MINIMA"
)

// caminhoNoDocumento diz onde vive, no documentoCompleto, o objecto de cada tipo cuja
// obrigatoriedade se mede. O Predicate fica de fora (ver os limites declarados acima).
var caminhoNoDocumento = []struct {
	tipo    reflect.Type
	caminho []any
}{
	{reflect.TypeOf(plan.PlanDocument{}), nil},
	{reflect.TypeOf(plan.Node{}), []any{"nodes", 0}},
	{reflect.TypeOf(plan.Node{}), []any{"nodes", 1}},
	{reflect.TypeOf(plan.PlannerMeta{}), []any{"planner_meta"}},
	{reflect.TypeOf(plan.BudgetEstimate{}), []any{"budget_total"}},
	{reflect.TypeOf(plan.ToolRef{}), []any{"nodes", 0, "tools", 0}},
	{reflect.TypeOf(plan.Output{}), []any{"nodes", 0, "outputs", 0}},
	{reflect.TypeOf(plan.ConditionalEdge{}), []any{"nodes", 1, "conditional_on", 0}},
	{reflect.TypeOf(plan.PayloadEdge{}), []any{"nodes", 1, "consumes", 0}},
}

// ondeSeDeclaraAninhado: para cada tipo aninhado, a secção e o campo-pai em cuja linha o
// template o declara entre chavetas.
var ondeSeDeclaraAninhado = []struct {
	tipo             reflect.Type
	seccao, campoPai string
}{
	{reflect.TypeOf(plan.PlannerMeta{}), cabecalhoTopo, "planner_meta"},
	{reflect.TypeOf(plan.BudgetEstimate{}), cabecalhoTopo, "budget_total"},
	{reflect.TypeOf(plan.BudgetEstimate{}), cabecalhoNo, "budget_estimate"},
	{reflect.TypeOf(plan.ToolRef{}), cabecalhoNo, "tools"},
	{reflect.TypeOf(plan.ConditionalEdge{}), cabecalhoNo, "conditional_on"},
	{reflect.TypeOf(plan.Output{}), cabecalhoNo, "outputs"},
	{reflect.TypeOf(plan.PayloadEdge{}), cabecalhoNo, "consumes"},
}

// camposJSON devolve os nomes JSON dos campos exportados de um tipo.
func camposJSON(t reflect.Type) []string {
	var out []string
	for i := 0; i < t.NumField(); i++ {
		nome := strings.Split(t.Field(i).Tag.Get("json"), ",")[0]
		if nome == "" || nome == "-" {
			continue
		}
		out = append(out, nome)
	}
	return out
}

// seguir desce no documento genérico pelo caminho dado.
func seguir(t *testing.T, doc any, caminho []any) map[string]any {
	t.Helper()
	cur := doc
	for _, passo := range caminho {
		switch p := passo.(type) {
		case string:
			cur = cur.(map[string]any)[p]
		case int:
			cur = cur.([]any)[p]
		}
	}
	obj, ok := cur.(map[string]any)
	if !ok {
		t.Fatalf("caminho %v nao e um objecto no documentoCompleto", caminho)
	}
	return obj
}

// obrigatoriedadePeloDecode mede, por tipo e campo, se o decode recusa o documento sem ele.
func obrigatoriedadePeloDecode(t *testing.T) map[string]map[string]bool {
	t.Helper()
	if _, err := plan.Decode([]byte(documentoCompleto)); err != nil {
		t.Fatalf("pre-condicao: o documentoCompleto tem de passar o decode, veio %v", err)
	}
	out := map[string]map[string]bool{}
	medidos := map[string]bool{}
	for _, alvo := range caminhoNoDocumento {
		tipo := alvo.tipo.Name()
		if out[tipo] == nil {
			out[tipo] = map[string]bool{}
		}
		for _, campo := range camposJSON(alvo.tipo) {
			var doc any
			if err := json.Unmarshal([]byte(documentoCompleto), &doc); err != nil {
				t.Fatal(err)
			}
			obj := seguir(t, doc, alvo.caminho)
			if _, ok := obj[campo]; !ok {
				continue // outro caminho do mesmo tipo mede-o
			}
			medidos[tipo+"."+campo] = true
			delete(obj, campo)
			raw, err := json.Marshal(doc)
			if err != nil {
				t.Fatal(err)
			}
			_, decErr := plan.Decode(raw)
			out[tipo][campo] = out[tipo][campo] || decErr != nil
		}
	}
	// Um campo novo que o documentoCompleto não use não pode escapar à medição em silêncio.
	for _, alvo := range caminhoNoDocumento {
		for _, campo := range camposJSON(alvo.tipo) {
			if !medidos[alvo.tipo.Name()+"."+campo] {
				t.Fatalf("o documentoCompleto nao usa %s.%s: a obrigatoriedade nao se mede", alvo.tipo.Name(), campo)
			}
		}
	}
	return out
}

// seccao devolve o texto entre dois cabeçalhos; "" se o de início não existir (é assim
// que um template sem bloco SCHEMA, como o 1.1.0, falha em todos os campos).
func seccao(template, inicio, fim string) string {
	ini := strings.Index(template, inicio)
	if ini < 0 {
		return ""
	}
	resto := template[ini+len(inicio):]
	if f := strings.Index(resto, fim); f >= 0 {
		return resto[:f]
	}
	return resto
}

// entrada devolve a declaração de um campo numa secção: a linha que começa pelo campo e as
// linhas de continuação mais indentadas. marcado diz se o campo leva `*`.
func entrada(sec, campo string) (texto string, marcado, existe bool) {
	re := regexp.MustCompile(`^(\s*)` + regexp.QuoteMeta(campo) + `(\*?)(\s|$)`)
	linhas := strings.Split(sec, "\n")
	for i, l := range linhas {
		m := re.FindStringSubmatch(l)
		if m == nil {
			continue
		}
		ind := len(m[1])
		partes := []string{l}
		for _, seg := range linhas[i+1:] {
			if len(seg)-len(strings.TrimLeft(seg, " ")) <= ind+2 || strings.TrimSpace(seg) == "" {
				break
			}
			partes = append(partes, seg)
		}
		return strings.Join(partes, "\n"), m[2] == "*", true
	}
	return "", false, false
}

// linhaQueComeca devolve a linha da secção que começa (sem indentação) pelo prefixo.
func linhaQueComeca(sec, prefixo string) string {
	for _, l := range strings.Split(sec, "\n") {
		if strings.HasPrefix(l, prefixo) {
			return l
		}
	}
	return ""
}

// chavetas devolve os nomes declarados entre as primeiras chavetas de uma entrada.
func chavetas(texto string) map[string]bool {
	ini, fim := strings.Index(texto, "{"), strings.Index(texto, "}")
	out := map[string]bool{}
	if ini < 0 || fim < ini {
		return out
	}
	for _, tok := range strings.Split(texto[ini+1:fim], ",") {
		out[strings.TrimSpace(tok)] = true
	}
	return out
}

// faltasNoTemplate devolve tudo o que o bloco SCHEMA não declara como o código exige.
func faltasNoTemplate(t *testing.T, template string) []string {
	t.Helper()
	topo := seccao(template, cabecalhoTopo, cabecalhoNo)
	no := seccao(template, cabecalhoNo, cabecalhoPred)
	pred := seccao(template, cabecalhoPred, cabecalhoForma)
	seccoes := map[string]string{cabecalhoTopo: topo, cabecalhoNo: no}
	obrig := obrigatoriedadePeloDecode(t)
	var faltas []string

	// Topo e nó: cada campo tem a sua linha, com `*` se e só se o decode o exige.
	for _, nivel := range []struct {
		nome, sec string
		tipo      reflect.Type
	}{
		{"topo", topo, reflect.TypeOf(plan.PlanDocument{})},
		{"no", no, reflect.TypeOf(plan.Node{})},
	} {
		for _, campo := range camposJSON(nivel.tipo) {
			_, marcado, existe := entrada(nivel.sec, campo)
			if quer := obrig[nivel.tipo.Name()][campo]; !existe || marcado != quer {
				faltas = append(faltas, nivel.nome+"."+campo+marca(quer))
			}
		}
	}

	// Aninhados: nas chavetas da linha do campo-pai, com `*` se e só se o decode o exige.
	for _, alvo := range ondeSeDeclaraAninhado {
		texto, _, _ := entrada(seccoes[alvo.seccao], alvo.campoPai)
		declarados := chavetas(texto)
		for _, campo := range camposJSON(alvo.tipo) {
			quer := obrig[alvo.tipo.Name()][campo]
			if !declarados[campo+marca(quer)] {
				faltas = append(faltas, alvo.campoPai+"."+campo+marca(quer))
			}
		}
	}

	// Predicado: subject e op obrigatórios; enum, metric e number nomeados.
	for campo, quer := range map[string]bool{"subject": true, "op": true, "enum": false, "metric": false, "number": false} {
		if _, marcado, existe := entrada(pred, campo); !existe || marcado != quer {
			faltas = append(faltas, "predicado."+campo+marca(quer))
		}
	}
	if got := camposJSON(reflect.TypeOf(plan.Predicate{})); len(got) != 5 {
		faltas = append(faltas, "predicado: campos mudaram "+strings.Join(got, ","))
	}

	// Valores fechados, cada grupo na linha do seu campo.
	riskTxt, _, _ := entrada(no, "risk_class")
	subjTxt, _, _ := entrada(pred, "subject")
	opTxt, _, _ := entrada(pred, "op")
	for _, grupo := range []struct {
		onde, texto string
		valores     []string
	}{
		{"risk_class", riskTxt, []string{string(plan.RiskSafe), string(plan.RiskGray), string(plan.RiskDanger)}},
		{"subject", subjTxt, []string{
			string(plan.SubjectTerminalState), string(plan.SubjectVerdict), string(plan.SubjectMetric),
			string(plan.EnumComplete), string(plan.EnumFailed), string(plan.EnumPass), string(plan.EnumFail),
		}},
		{"op", opTxt, []string{string(plan.OpEq), string(plan.OpNe), string(plan.OpLt), string(plan.OpLte), string(plan.OpGt), string(plan.OpGte)}},
		{"type", linhaQueComeca(pred, "type de outputs e consumes:"), []string{
			string(plan.PayloadSummary), string(plan.PayloadRecord), string(plan.PayloadArtifact), string(plan.PayloadMetrics), string(plan.PayloadVerdict),
		}},
		{"taint", linhaQueComeca(pred, "taint de outputs:"), []string{string(plan.TaintTrusted), string(plan.TaintUntrusted)}},
	} {
		for _, v := range grupo.valores {
			if !strings.Contains(grupo.texto, `"`+v+`"`) {
				faltas = append(faltas, grupo.onde+`="`+v+`"`)
			}
		}
	}
	sort.Strings(faltas)
	return faltas
}

func marca(obrigatorio bool) string {
	if obrigatorio {
		return "*"
	}
	return ""
}

// TestTemplateDeclaraOSchemaQueODecodeExige: o template corrente declara todos os campos,
// com a obrigatoriedade certa e no sítio certo, e todos os valores fechados. O template
// 1.1.0 (o que falhou em produção) não tem bloco nenhum e falha em tudo.
func TestTemplateDeclaraOSchemaQueODecodeExige(t *testing.T) {
	if faltas := faltasNoTemplate(t, Current.Template); len(faltas) > 0 {
		t.Fatalf("o template %s nao declara: %v", Current.MetaPromptVersion(), faltas)
	}
	faltas := faltasNoTemplate(t, lerTemplate110(t))
	for _, quer := range []string{"topo.objective*", "topo.budget_total", "topo.nodes*", "no.objective*"} {
		if !contem(faltas, quer) {
			t.Fatalf("o template 1.1.0 devia falhar em %q; faltas medidas: %v", quer, faltas)
		}
	}
}

// TestAOS400_SemOObjectiveDeTopoAFaltaEExactamenteEssa: FALHA-ANTES isolada. O template
// corrente sem a linha do objective de topo — a lacuna que falhou em produção — falha
// exactamente nesse campo; o objective dos nós não a tapa.
func TestAOS400_SemOObjectiveDeTopoAFaltaEExactamenteEssa(t *testing.T) {
	linha := "  objective*       string nao vazia: o objectivo do plano inteiro\n"
	if strings.Count(Current.Template, linha) != 1 {
		t.Fatalf("pre-condicao: a linha do objective de topo mudou; actualizar o mutante")
	}
	mutante := strings.Replace(Current.Template, linha, "", 1)
	if faltas := faltasNoTemplate(t, mutante); len(faltas) != 1 || faltas[0] != "topo.objective*" {
		t.Fatalf("sem o objective de topo, a falta devia ser so topo.objective*; veio %v", faltas)
	}
}

// formaMinima extrai o JSON da FORMA MINIMA do template.
func formaMinima(t *testing.T) string {
	t.Helper()
	resto := seccao(Current.Template, cabecalhoForma, "\n\n")
	abre := strings.Index(resto, "{")
	if resto == "" || abre < 0 {
		t.Fatalf("o template nao tem a FORMA MINIMA com JSON:\n%s", Current.Template)
	}
	return resto[abre:]
}

// TestAOS400_FormaMinimaPassaODecodeEOValidador: a forma que o template mostra ao modelo
// passa o decode tal como está, e, com os <...> preenchidos a partir do snapshot e do
// contexto, passa o validador AOS-231 — que é quem acaba o run sem nova tentativa.
func TestAOS400_FormaMinimaPassaODecodeEOValidador(t *testing.T) {
	forma := formaMinima(t)
	if _, err := plan.Decode([]byte(forma)); err != nil {
		t.Fatalf("a FORMA MINIMA do template nao passa o decode: %v\n%s", err, forma)
	}
	snap := testSnapshot()
	tool := snap.Tools[0]
	preenchida := strings.NewReplacer(
		"<name do snapshot>", tool.Name,
		"<version do snapshot>", tool.Version,
		"<digest do snapshot>", tool.Digest,
		`"capabilities_hash":"<do contexto>"`, `"capabilities_hash":"`+snap.Hash+`"`,
	).Replace(forma)
	doc, err := plan.Decode([]byte(preenchida))
	if err != nil {
		t.Fatalf("decode da FORMA MINIMA preenchida: %v", err)
	}
	if v := planvalidate.Validate(doc, snap, testCeilings()); !v.OK {
		t.Fatalf("a FORMA MINIMA preenchida devia passar o validador: %+v", v)
	}
	for _, n := range doc.Nodes {
		if n.BudgetEstimate.Tokens == 0 {
			t.Fatalf("a FORMA MINIMA nao pode ensinar orcamentos a zero (no %s)", n.NodeID)
		}
	}
}

// TestAOS400_DocumentoDaProducaoERecusadoSemObjectiveDeTopo documenta o lado do decode: a
// forma do erro medido em produção (nós com objective, topo sem ele) é recusada com
// ErrMissingObjective. Não depende do template — a falha-antes do prompt está acima.
func TestAOS400_DocumentoDaProducaoERecusadoSemObjectiveDeTopo(t *testing.T) {
	var doc map[string]any
	if err := json.Unmarshal([]byte(documentoCompleto), &doc); err != nil {
		t.Fatal(err)
	}
	delete(doc, "objective")
	raw, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := plan.Decode(raw); !errors.Is(err, plan.ErrMissingObjective) {
		t.Fatalf("esperava plan.ErrMissingObjective, veio %v", err)
	}
}

// fingerprintPrompt110 é o SHA-256 do template 1.1.0 tal como foi publicado (AOS-273): o
// texto exacto, sem newline final. Prende o testdata ao texto real.
const fingerprintPrompt110 = "25c9732a64223b8304126e4593cd122bb5fa729860df76a09599f0a8a8e8c54d"

func lerTemplate110(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile("testdata/prompt-1.1.0.txt")
	if err != nil {
		t.Fatalf("template 1.1.0: %v", err)
	}
	// Um editor que acrescente o newline final (.editorconfig) não muda o template.
	raw = bytes.TrimSuffix(raw, []byte("\n"))
	sum := sha256.Sum256(raw)
	if got := hex.EncodeToString(sum[:]); got != fingerprintPrompt110 {
		t.Fatalf("testdata/prompt-1.1.0.txt nao e o template publicado: sha256=%s", got)
	}
	return string(raw)
}

// TestAOS400_Mutacao110Para120PassaOGateADR012: a primeira chamada do gate sobre a mutação
// REAL (os bumps anteriores só o exercitavam com templates inventados). 1.1.0 → 1.2.0 é um
// MINOR: mesmo MAJOR, MINOR seguinte, PATCH a zero, e as regras do 1.1.0 ficam intactas.
//
// AOS-415: o `Current` subiu para 1.3.0, pelo que esta mutação passa a ser medida contra o
// template 1.2.0 GUARDADO (`testdata/prompt-1.2.0.txt`) — o que este teste prova continua a
// ser o bump do AOS-400, não o do ticket seguinte. O bump 1.2.0 → 1.3.0 tem teste próprio.
func TestAOS400_Mutacao110Para120PassaOGateADR012(t *testing.T) {
	antigo := Prompt{Version: PromptVersion{Major: 1, Minor: 1, Patch: 0}, Template: lerTemplate110(t)}
	novo := Prompt{Version: PromptVersion{Major: 1, Minor: 2, Patch: 0}, Template: lerTemplate120(t)}
	ap := PromptApproval{Approver: "Arquitecto de Plataforma", ADR012Ref: "ADR-012 (AOS-400)"}
	if err := ValidatePromptMutation(antigo, novo, ap); err != nil {
		t.Fatalf("a mutacao 1.1.0 -> 1.2.0 devia passar o gate: %v", err)
	}
	inicioRegras := strings.Index(antigo.Template, "REGRAS DURAS:")
	if inicioRegras < 0 || !strings.Contains(novo.Template, antigo.Template[inicioRegras:]) {
		t.Fatal("as REGRAS DURAS do 1.1.0 tinham de ficar intactas no 1.2.0 (bump MINOR aditivo)")
	}
}

// fingerprintPrompt120 é o SHA-256 do template 1.2.0 tal como foi publicado (AOS-400).
const fingerprintPrompt120 = "07c2ae7b10992476ca812ec6b6c0ef9a0e04f8813f0476e69405858990205c2e"

func lerTemplate120(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile("testdata/prompt-1.2.0.txt")
	if err != nil {
		t.Fatalf("template 1.2.0: %v", err)
	}
	raw = bytes.TrimSuffix(raw, []byte("\n"))
	sum := sha256.Sum256(raw)
	if got := hex.EncodeToString(sum[:]); got != fingerprintPrompt120 {
		t.Fatalf("testdata/prompt-1.2.0.txt nao e o template publicado: sha256=%s", got)
	}
	return string(raw)
}

// TestAOS415_Mutacao120Para130PassaOGateADR012: o bump que acrescenta a regra 11 (o bloco
// de RECUSA DA TENTATIVA ANTERIOR). MINOR e ADITIVO: as regras 1 a 10 do 1.2.0 ficam
// intactas, e o que muda é texto NOVO no fim.
func TestAOS415_Mutacao120Para130PassaOGateADR012(t *testing.T) {
	antigo := Prompt{Version: PromptVersion{Major: 1, Minor: 2, Patch: 0}, Template: lerTemplate120(t)}
	ap := PromptApproval{Approver: "Arquitecto de Plataforma", ADR012Ref: "ADR-012 (AOS-415)"}
	if err := ValidatePromptMutation(antigo, Current, ap); err != nil {
		t.Fatalf("a mutacao 1.2.0 -> %s devia passar o gate: %v", Current.MetaPromptVersion(), err)
	}
	if v := Current.Version; v.Major != 1 || v.Minor != 3 || v.Patch != 0 {
		t.Fatalf("o AOS-415 e um MINOR sobre 1.2.0; Current=%s", Current.MetaPromptVersion())
	}
	inicioRegras := strings.Index(antigo.Template, "REGRAS DURAS:")
	if inicioRegras < 0 || !strings.Contains(Current.Template, antigo.Template[inicioRegras:]) {
		t.Fatal("as REGRAS DURAS do 1.2.0 tinham de ficar intactas no 1.3.0 (bump MINOR aditivo)")
	}
	if !strings.Contains(Current.Template, "RECUSA DA TENTATIVA ANTERIOR") {
		t.Fatal("o 1.3.0 tem de declarar o bloco de recusa que o chamador injecta")
	}
}

func contem(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}
