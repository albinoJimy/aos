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
	"strconv"
	"strings"
	"testing"

	"github.com/aos-ref/control-plane/orchestrator/plan"
	"github.com/aos-ref/control-plane/orchestrator/planvalidate"
)

// documentoCompleto usa todos os campos dos tipos cuja obrigatoriedade se mede por remoção
// (na linha 1.3.0, para caberem outputs, consumes e a origem declarada de uma saída). É válido
// para o DECODE, que é o que se mede aqui; não para o validador (analise liga-se a recolha
// pelos dois canais).
const documentoCompleto = `{
  "plan_version": "1.3.0",
  "objective": "recolher e analisar",
  "budget_total": {"tokens": 100, "cost_micro_usd": 100},
  "planner_meta": {"model": "m", "prompt_version": "1.2.0", "capabilities_hash": "sha256:x"},
  "nodes": [
    {"node_id": "recolha", "role": "worker", "objective": "recolher",
     "tools": [{"name": "fs.read", "version": "1.0.0", "digest": "sha256:aaa"}],
     "depends_on": [],
     "budget_estimate": {"tokens": 10, "cost_micro_usd": 10},
     "risk_class": "safe",
     "outputs": [{"name": "dados", "type": "record", "taint": "untrusted", "from_tool": "fs.read"}]},
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

// faltasNoTemplate devolve tudo o que o bloco SCHEMA não declara como o código exige. NÃO HÁ
// EXCEPÇÕES: a que o AOS-500 deixou para `outputs.from_tool` saiu com o prompt 1.5.0 (AOS-501),
// que nomeia o campo.
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

// TestTemplateDeclaraOSchemaQueODecodeExige: o template que declara o schema INTEIRO — o 1.5.0
// ([WithOutputSource], AOS-501) — declara todos os campos, com a obrigatoriedade certa e no
// sítio certo, e todos os valores fechados, sem excepção nenhuma. Ao corrente (1.4.0) falta
// EXACTAMENTE `outputs.from_tool`: é por isso que só o 1.5.0 pode instruir um planeador cujos
// planos com origem correm. O template 1.1.0 (o que falhou em produção) não tem bloco nenhum e
// falha em tudo.
func TestTemplateDeclaraOSchemaQueODecodeExige(t *testing.T) {
	if faltas := faltasNoTemplate(t, WithOutputSource.Template); len(faltas) > 0 {
		t.Fatalf("o template %s nao declara: %v", WithOutputSource.MetaPromptVersion(), faltas)
	}
	if faltas := faltasNoTemplate(t, Current.Template); len(faltas) != 1 || faltas[0] != "outputs.from_tool" {
		t.Fatalf("ao template corrente (%s) tem de faltar so outputs.from_tool; faltas medidas: %v", Current.MetaPromptVersion(), faltas)
	}
	if strings.Contains(Current.Template, "from_tool") {
		t.Fatalf("o template corrente (%s) nao pode nomear from_tool: fora de `on` o planeador nao e instruido a emiti-lo", Current.MetaPromptVersion())
	}
	faltas := faltasNoTemplate(t, lerTemplate110(t))
	for _, quer := range []string{"topo.objective*", "topo.budget_total", "topo.nodes*", "no.objective*"} {
		if !contem(faltas, quer) {
			t.Fatalf("o template 1.1.0 devia falhar em %q; faltas medidas: %v", quer, faltas)
		}
	}
}

// fingerprintPrompt140 é o SHA-256 do template 1.4.0 (AOS-484), o corrente. Tirado antes de
// qualquer alteração do AOS-500, e o AOS-501 não lhe toca.
const fingerprintPrompt140 = "51393c16f3b22337ea71b016c1e21a4ea0ab9b4cb624676f5d7a2bb0192fb5c8"

// fingerprintPrompt150 é o SHA-256 do template 1.5.0 (AOS-501), tal como este ticket o publica.
const fingerprintPrompt150 = "8d33873417075208e36321f1ceb504f0fa799f100b4df0b24e9cc41dc4bfd746"

// TestAOS501_OsDoisPromptsTemOsBytesFixados: o binário conhece DUAS versões do prompt de
// decomposição, e os bytes das duas estão presos aqui.
//
//   - [Current] é a 1.4.0, byte a byte a de antes do AOS-500 e do AOS-501: é a de omissão de
//     `decompose.New`, e a que o planeador recebe com a entrega por referência desligada;
//   - [WithOutputSource] é a 1.5.0, que nomeia `from_tool`, e só se usa por escolha explícita.
//
// Mudar um byte de qualquer delas tem de passar por aqui.
func TestAOS501_OsDoisPromptsTemOsBytesFixados(t *testing.T) {
	for _, c := range []struct {
		p           Prompt
		versao, sha string
	}{
		{Current, "1.4.0", fingerprintPrompt140},
		{WithOutputSource, "1.5.0", fingerprintPrompt150},
	} {
		if got := c.p.MetaPromptVersion(); got != c.versao {
			t.Fatalf("a versao do prompt e %s; fixada em %s", got, c.versao)
		}
		if got := c.p.Fingerprint(); got != c.sha {
			t.Fatalf("os bytes do template %s mudaram: sha256=%s, fixado em %s", c.versao, got, c.sha)
		}
	}
}

// regra13 é o texto da regra que o AOS-501 acrescenta, por extenso, pela razão da regra 12: o
// que ela DIZ ao modelo é isto, e uma edição que lhe mude uma palavra tem de passar por aqui.
const regra13 = `13. from_tool declara a ORIGEM de uma saida: o no seguinte recebe o que a ferramenta
    devolveu, e NAO o texto que o no escreveu. Declara-o num output quando o no existe
    para ir buscar um conteudo (ler um documento, obter um registo) e o consumidor
    precisa desse conteudo inteiro, sem transformacao. NAO o declares quando o
    consumidor precisa do que o no CONCLUIU (resumir, extrair, classificar, decidir):
    ai a saida e o texto do no, sem from_tool. Condicoes, todas obrigatorias: o valor e
    o name de uma ferramenta de tools desse mesmo no, e esse name aparece uma so vez em
    tools; o type do output e "record" ou "artifact"; o no nao e role: verifier e nao
    tem consumes; no maximo UM output do no usa from_tool. O no tem de chamar essa
    ferramenta UMA so vez, na primeira resposta: sem essa chamada, ou com duas, o no
    fica failed e o consumidor nao corre. Usar from_tool obriga a carimbar plan_version
    "1.3.0" (regra 6), e nao "1.2.0".`

// TestAOS501_OPrompt150EOPrompt140ComQuatroEdicoes: o 1.5.0 é o 1.4.0 com QUATRO edições, e
// mais nada — o campo na linha de `outputs`, a linha que o explica, a linha 1.3.0 na regra 6, e
// a regra 13 no fim. Refaz-se o 1.5.0 a partir do 1.4.0 e exige-se a igualdade byte a byte: uma
// quinta diferença, em qualquer sítio, falha aqui.
//
// É um MINOR e passa o gate ADR-012 com aprovação; sem ela é recusado.
func TestAOS501_OPrompt150EOPrompt140ComQuatroEdicoes(t *testing.T) {
	refeito := Current.Template
	for _, e := range [][2]string{
		{"  outputs          lista de {name*, type*, taint} (no maximo 8)",
			"  outputs          lista de {name*, type*, taint, from_tool} (no maximo 8)"},
		{"taint de outputs: \"trusted\" ou \"untrusted\"; omite se nao tiveres base.\n",
			"taint de outputs: \"trusted\" ou \"untrusted\"; omite se nao tiveres base.\n" +
				"from_tool de outputs: o name de UMA ferramenta de tools do mesmo no (regra 13); omite\n" +
				"nos outros casos.\n"},
		{"usa outputs, consumes ou o papel reservado role: verifier. Carimbar abaixo da linha\n" +
			"   que usas e RECUSADO (plan_version_below_features); carimbar acima da linha corrente\n" +
			"   tambem.",
			"usa outputs, consumes ou o papel reservado role: verifier; \"1.3.0\" se algum output\n" +
				"   usa from_tool. Carimbar abaixo da linha que usas e RECUSADO\n" +
				"   (plan_version_below_features); carimbar acima da linha corrente tambem."},
	} {
		if strings.Count(refeito, e[0]) != 1 {
			t.Fatalf("pre-condicao: o 1.4.0 tem exactamente uma vez o troco %q", e[0])
		}
		refeito = strings.Replace(refeito, e[0], e[1], 1)
	}
	refeito += "\n" + regra13
	if WithOutputSource.Template != refeito {
		t.Fatalf("o 1.5.0 tem de ser o 1.4.0 com as quatro edicoes do AOS-501 e mais nada:\n%s", WithOutputSource.Template)
	}
	ap := PromptApproval{Approver: "Arquitecto de Plataforma", ADR012Ref: "ADR-012 (AOS-501)"}
	if err := ValidatePromptMutation(Current, WithOutputSource, ap); err != nil {
		t.Fatalf("a mutacao %s -> %s devia passar o gate: %v", Current.MetaPromptVersion(), WithOutputSource.MetaPromptVersion(), err)
	}
	if v := WithOutputSource.Version; v.Major != 1 || v.Minor != 5 || v.Patch != 0 {
		t.Fatalf("o AOS-501 e um MINOR sobre 1.4.0; e %s", WithOutputSource.MetaPromptVersion())
	}
	if err := ValidatePromptMutation(Current, WithOutputSource, PromptApproval{}); !errors.Is(err, ErrPromptUnapproved) {
		t.Fatalf("sem aprovacao a mutacao tinha de ser recusada com ErrPromptUnapproved, veio %v", err)
	}
	// As regras 1 a 5 e 7 a 12 ficam byte a byte: só a 6 ganha uma linha.
	for _, n := range []string{"1. ", "2. ", "3. ", "4. ", "5. ", "7. ", "8. ", "9. ", "10. ", "11. ", "12. "} {
		de := strings.Index(Current.Template, "\n"+n)
		if de < 0 {
			t.Fatalf("pre-condicao: o 1.4.0 tem a regra %q", n)
		}
		ate := strings.Index(Current.Template[de+1:], "\n"+proximaRegra(n))
		regra := Current.Template[de:]
		if ate >= 0 {
			regra = Current.Template[de : de+1+ate]
		}
		if !strings.Contains(WithOutputSource.Template, regra) {
			t.Fatalf("a regra %q do 1.4.0 tinha de ficar intacta no 1.5.0", n)
		}
	}
}

// proximaRegra devolve o prefixo da regra a seguir a `n` ("12. " ⇒ "13. ").
func proximaRegra(n string) string {
	num := 0
	for _, c := range n {
		if c < '0' || c > '9' {
			break
		}
		num = num*10 + int(c-'0')
	}
	return strconv.Itoa(num+1) + ". "
}

// TestAOS501_ARegra13DizOQueOValidadorSustenta prende as afirmações da regra ao validador
// (AOS-500): o plano que ela ensina passa; cada condição que ela dá por obrigatória é recusada
// com o sub-código próprio quando violada; e o carimbo que ela manda é o que o validador exige.
//
// LIMITE DECLARADO: «o no seguinte recebe o que a ferramenta devolveu» e «sem essa chamada, ou
// com duas, o no fica failed» são comportamento do `aos-orq` e do kernel do nó, noutros
// módulos. Quem as prende: `TestAOS501ComOBinarioReal` e `TestAOS501_EntregaDoRun`, em
// `packages/cmd/aos-orq`.
func TestAOS501_ARegra13DizOQueOValidadorSustenta(t *testing.T) {
	snap := testSnapshot()
	tool := snap.Tools[0]
	ref := `{"name":"` + tool.Name + `","version":"` + tool.Version + `","digest":"` + tool.Digest + `"}`
	plano := func(carimbo, papelDoLeitor, toolsDoLeitor, saidaDoLeitor, consumesDoLeitor string) plan.PlanDocument {
		t.Helper()
		raw := `{"plan_version":"` + carimbo + `","objective":"ler e resumir",
 "budget_total":{"tokens":300,"cost_micro_usd":300},
 "planner_meta":{"model":"m","prompt_version":"` + WithOutputSource.MetaPromptVersion() + `","capabilities_hash":"` + snap.Hash + `"},
 "nodes":[
  {"node_id":"n0","role":"reader","objective":"preparar",
   "tools":[],"depends_on":[],"budget_estimate":{"tokens":100,"cost_micro_usd":100},
   "outputs":[{"name":"base","type":"record"}]},
  {"node_id":"n1","role":"` + papelDoLeitor + `","objective":"ler o documento",
   "tools":[` + toolsDoLeitor + `],"depends_on":["n0"],"budget_estimate":{"tokens":100,"cost_micro_usd":100},
   "outputs":[` + saidaDoLeitor + `]` + consumesDoLeitor + `},
  {"node_id":"n2","role":"summarizer","objective":"resumir o que foi lido",
   "tools":[],"depends_on":["n1"],"budget_estimate":{"tokens":100,"cost_micro_usd":100}}]}`
		doc, err := plan.Decode([]byte(raw))
		if err != nil {
			t.Fatalf("o plano de teste nao passa o decode: %v\n%s", err, raw)
		}
		return doc
	}
	comOrigem := `{"name":"conteudo","type":"record","from_tool":"` + tool.Name + `"}`

	// O plano que a regra ensina passa — com `record` e com `artifact`.
	for _, saida := range []string{comOrigem, strings.Replace(comOrigem, `"record"`, `"artifact"`, 1)} {
		if v := planvalidate.Validate(plano("1.3.0", "reader", ref, saida, ""), snap, testCeilings()); !v.OK {
			t.Fatalf("o plano que a regra 13 ensina devia passar o validador: %+v", v)
		}
	}
	// «Usar from_tool obriga a carimbar plan_version "1.3.0" (regra 6), e nao "1.2.0"».
	if !strings.Contains(regra13, `plan_version
    "1.3.0" (regra 6), e nao "1.2.0"`) {
		t.Fatal("a regra 13 tem de dizer o carimbo que from_tool obriga")
	}
	if v := planvalidate.Validate(plano("1.2.0", "reader", ref, comOrigem, ""), snap, testCeilings()); v.Reason != planvalidate.ReasonVersionBelowFeatures {
		t.Fatalf("from_tool carimbado 1.2.0 tinha de ser recusado com %s: %+v", planvalidate.ReasonVersionBelowFeatures, v)
	}
	// Cada condição «obrigatória» da regra, violada, é recusada com o seu sub-código.
	outraRef := `{"name":"` + tool.Name + `","version":"` + tool.Version + `","digest":"` + tool.Digest + `"}`
	for nome, c := range map[string]struct {
		doc   plan.PlanDocument
		razao planvalidate.Reason
		frase string
	}{
		"tool que nao e do no": {plano("1.3.0", "reader", "", comOrigem, ""),
			planvalidate.ReasonFromToolUnknownTool, "o name de uma ferramenta de tools desse mesmo no"},
		"tool referida duas vezes": {plano("1.3.0", "reader", ref+","+outraRef, comOrigem, ""),
			planvalidate.ReasonFromToolAmbiguousTool, "esse name aparece uma so vez em\n    tools"},
		"tipo summary": {plano("1.3.0", "reader", ref, strings.Replace(comOrigem, `"record"`, `"summary"`, 1), ""),
			planvalidate.ReasonFromToolOutputType, `o type do output e "record" ou "artifact"`},
		"no com consumes": {plano("1.3.0", "reader", ref, comOrigem, `,"consumes":[{"from":"n0","output":"base","type":"record"}]`),
			planvalidate.ReasonFromToolWithConsumes, "nao\n    tem consumes"},
		"duas saidas com origem": {plano("1.3.0", "reader", ref, comOrigem+`,{"name":"copia","type":"record","from_tool":"`+tool.Name+`"}`, ""),
			planvalidate.ReasonFromToolMultiple, "no maximo UM output do no usa from_tool"},
	} {
		if !strings.Contains(regra13, c.frase) {
			t.Errorf("%s: a regra 13 deixou de dizer %q", nome, c.frase)
		}
		if v := planvalidate.Validate(c.doc, snap, testCeilings()); !v.Rejected() || v.Reason != c.razao {
			t.Errorf("%s: tinha de ser recusado com %s; veio %+v", nome, c.razao, v)
		}
	}
	if !strings.Contains(regra13, "o no nao e role: verifier") {
		t.Error("a regra 13 tem de dizer que um verifier nao declara a origem")
	}
}

// TestAOS400_SemOObjectiveDeTopoAFaltaEExactamenteEssa: FALHA-ANTES isolada. O template
// corrente sem a linha do objective de topo — a lacuna que falhou em produção — falha
// exactamente nesse campo; o objective dos nós não a tapa.
func TestAOS400_SemOObjectiveDeTopoAFaltaEExactamenteEssa(t *testing.T) {
	linha := "  objective*       string nao vazia: o objectivo do plano inteiro\n"
	// Parte do template que declara o schema INTEIRO (o 1.5.0): a falta tem de ser só a que o
	// mutante cria. A linha é igual nas duas versões.
	if strings.Count(WithOutputSource.Template, linha) != 1 || strings.Count(Current.Template, linha) != 1 {
		t.Fatalf("pre-condicao: a linha do objective de topo mudou; actualizar o mutante")
	}
	mutante := strings.Replace(WithOutputSource.Template, linha, "", 1)
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
//
// AOS-484: o `Current` subiu para 1.4.0, pelo que esta mutação passa a ser medida contra o
// template 1.3.0 GUARDADO (`testdata/prompt-1.3.0.txt`) — o que este teste prova continua a
// ser o bump do AOS-415, não o do ticket seguinte. O bump 1.3.0 → 1.4.0 tem teste próprio.
func TestAOS415_Mutacao120Para130PassaOGateADR012(t *testing.T) {
	antigo := Prompt{Version: PromptVersion{Major: 1, Minor: 2, Patch: 0}, Template: lerTemplate120(t)}
	novo := Prompt{Version: PromptVersion{Major: 1, Minor: 3, Patch: 0}, Template: lerTemplate130(t)}
	ap := PromptApproval{Approver: "Arquitecto de Plataforma", ADR012Ref: "ADR-012 (AOS-415)"}
	if err := ValidatePromptMutation(antigo, novo, ap); err != nil {
		t.Fatalf("a mutacao 1.2.0 -> 1.3.0 devia passar o gate: %v", err)
	}
	inicioRegras := strings.Index(antigo.Template, "REGRAS DURAS:")
	if inicioRegras < 0 || !strings.Contains(novo.Template, antigo.Template[inicioRegras:]) {
		t.Fatal("as REGRAS DURAS do 1.2.0 tinham de ficar intactas no 1.3.0 (bump MINOR aditivo)")
	}
	if !strings.Contains(novo.Template, "RECUSA DA TENTATIVA ANTERIOR") {
		t.Fatal("o 1.3.0 tem de declarar o bloco de recusa que o chamador injecta")
	}
}

// fingerprintPrompt130 é o SHA-256 do template 1.3.0 tal como foi publicado (AOS-415).
const fingerprintPrompt130 = "86feca621430859d2dd7b4386d3cd731f7dcc8b8c3dd01573ba5b98b5b50181a"

func lerTemplate130(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile("testdata/prompt-1.3.0.txt")
	if err != nil {
		t.Fatalf("template 1.3.0: %v", err)
	}
	raw = bytes.TrimSuffix(raw, []byte("\n"))
	sum := sha256.Sum256(raw)
	if got := hex.EncodeToString(sum[:]); got != fingerprintPrompt130 {
		t.Fatalf("testdata/prompt-1.3.0.txt nao e o template publicado: sha256=%s", got)
	}
	return string(raw)
}

// regra12 é o texto da regra que o AOS-484 acrescenta, tal como o template o publica. Fica
// aqui por extenso de propósito: o gate ADR-012 só vê que o conteúdo mudou e a versão subiu, e
// o teste de aditividade só vê que o texto antigo continua lá. O que a regra DIZ ao modelo é
// isto, e uma edição que lhe mude uma palavra tem de passar por aqui.
const regra12 = `12. depends_on sozinho fixa a ORDEM e NAO entrega dados. Um no que precise do que outro
    no produziu (o texto lido, o registo, o artefacto) so o recebe por contrato: o
    produtor declara-o em outputs e o consumidor declara-o em consumes. Para estes dois
    campos isto prevalece sobre o "SO quando o objectivo os exige" da regra 7, e usa-los
    obriga a carimbar plan_version "1.2.0" (regra 6). O executor so transporta duas
    coisas: de um no que nao e verifier, UM output de forma aberta ("summary", "record"
    ou "artifact"), e com mais do que um nao transporta nenhum; de um no role: verifier,
    o "verdict". Um consumes de "metrics" NAO e entregue, venha de que no vier: o no que
    o declara nao corre e fica failed. Um no com ferramenta de efeito continua sob a
    regra 8. depends_on sem consumes continua valido quando a dependencia e so de ordem.`

// TestAOS484_Mutacao130Para140PassaOGateADR012: o bump que acrescenta a regra 12 (os dados
// entre nós viajam por `outputs`/`consumes`, e o que o executor consegue transportar). MINOR
// e ADITIVO, e aqui «aditivo» mede-se byte a byte: o template 1.4.0 É o 1.3.0 seguido da
// regra nova — nada antes dela mudou, nem o SCHEMA, nem a FORMA MINIMA, nem as regras 1 a 11.
func TestAOS484_Mutacao130Para140PassaOGateADR012(t *testing.T) {
	antigo := Prompt{Version: PromptVersion{Major: 1, Minor: 3, Patch: 0}, Template: lerTemplate130(t)}
	ap := PromptApproval{Approver: "Arquitecto de Plataforma", ADR012Ref: "ADR-012 (AOS-484)"}
	if err := ValidatePromptMutation(antigo, Current, ap); err != nil {
		t.Fatalf("a mutacao 1.3.0 -> %s devia passar o gate: %v", Current.MetaPromptVersion(), err)
	}
	if v := Current.Version; v.Major != 1 || v.Minor != 4 || v.Patch != 0 {
		t.Fatalf("o AOS-484 e um MINOR sobre 1.3.0; Current=%s", Current.MetaPromptVersion())
	}
	inicioRegras := strings.Index(antigo.Template, "REGRAS DURAS:")
	if inicioRegras < 0 || !strings.Contains(Current.Template, antigo.Template[inicioRegras:]) {
		t.Fatal("as REGRAS DURAS do 1.3.0 tinham de ficar intactas no 1.4.0 (bump MINOR aditivo)")
	}
	if Current.Template != antigo.Template+"\n"+regra12 {
		t.Fatalf("o 1.4.0 tem de ser o 1.3.0 byte a byte, seguido da regra 12 e de mais nada:\n%s", Current.Template)
	}
	// Sem a aprovação ADR-012 a mesma mutação é recusada: o gate não é decorativo.
	if err := ValidatePromptMutation(antigo, Current, PromptApproval{}); !errors.Is(err, ErrPromptUnapproved) {
		t.Fatalf("sem aprovacao a mutacao tinha de ser recusada com ErrPromptUnapproved, veio %v", err)
	}
}

// TestAOS484_ARegra12DizOQueOSchemaEOValidadorSustentam prende as afirmações da regra ao
// código que as torna verdadeiras. A regra é texto para um modelo; se o contrato em código
// mudar e ela não, passa a ensinar um plano que o resto do sistema não cumpre.
//
// LIMITE DECLARADO: o que o EXECUTOR transporta é comportamento do `publicarSaidas` do `aos-orq`,
// noutro módulo — este teste não o alcança. Quem prende essas frases ao executor, em
// `packages/cmd/aos-orq`: `TestAOS484_DoisOutputsAbertosNaoSeTransportam` («com mais do que um
// nao transporta nenhum»), `TestAOS484_MetricsNaoSeTransportaNemDeUmVerificador` («um consumes de
// metrics NAO e entregue, venha de que no vier») e `TestAOS414_OVerificadorRecebeOQueONoAnteriorLeu`
// («de um no role: verifier, o verdict»).
func TestAOS484_ARegra12DizOQueOSchemaEOValidadorSustentam(t *testing.T) {
	// (1) Os tipos «de forma aberta» que a regra nomeia são exactamente os que o schema não
	// trata como forma fechada, e os outros dois são os fechados.
	for _, tipo := range []plan.PayloadType{plan.PayloadSummary, plan.PayloadRecord, plan.PayloadArtifact} {
		if tipo.ClosedForm() {
			t.Fatalf("a regra 12 chama forma aberta a %q, e o schema trata-o como fechado", tipo)
		}
		if !strings.Contains(regra12, `"`+string(tipo)+`"`) {
			t.Fatalf("a regra 12 nao nomeia o tipo de forma aberta %q", tipo)
		}
	}
	for _, tipo := range []plan.PayloadType{plan.PayloadMetrics, plan.PayloadVerdict} {
		if !tipo.ClosedForm() {
			t.Fatalf("a regra 12 trata %q como forma fechada, e o schema nao o trata assim", tipo)
		}
		if !strings.Contains(regra12, `"`+string(tipo)+`"`) {
			t.Fatalf("a regra 12 nao nomeia o tipo de forma fechada %q", tipo)
		}
	}
	// (2) O plano que a regra ensina — o produtor declara o output, o consumidor declara o
	// consumes, linha 1.2.0 — passa o decode e o validador AOS-231.
	snap := testSnapshot()
	tool := snap.Tools[0]
	contratoDoProdutor := `,
   "outputs":[{"name":"conteudo","type":"record","taint":"untrusted"}]`
	contratoDoConsumidor := `,
   "consumes":[{"from":"n1","output":"conteudo","type":"record"}]`
	comContrato := `{"plan_version":"1.2.0","objective":"ler e resumir",
 "budget_total":{"tokens":200,"cost_micro_usd":200},
 "planner_meta":{"model":"m","prompt_version":"` + Current.MetaPromptVersion() + `","capabilities_hash":"` + snap.Hash + `"},
 "nodes":[
  {"node_id":"n1","role":"reader","objective":"ler o documento",
   "tools":[{"name":"` + tool.Name + `","version":"` + tool.Version + `","digest":"` + tool.Digest + `"}],
   "depends_on":[],"budget_estimate":{"tokens":100,"cost_micro_usd":100}` + contratoDoProdutor + `},
  {"node_id":"n2","role":"summarizer","objective":"resumir o que foi lido",
   "tools":[],"depends_on":["n1"],"budget_estimate":{"tokens":100,"cost_micro_usd":100}` + contratoDoConsumidor + `}]}`
	doc, err := plan.Decode([]byte(comContrato))
	if err != nil {
		t.Fatalf("o plano que a regra 12 ensina nao passa o decode: %v", err)
	}
	if v := planvalidate.Validate(doc, snap, testCeilings()); !v.OK {
		t.Fatalf("o plano que a regra 12 ensina devia passar o validador: %+v", v)
	}
	if len(doc.Nodes[0].Outputs) != 1 || len(doc.Nodes[1].Consumes) != 1 {
		t.Fatalf("pre-condicao: o plano com contrato tinha de declarar um output e um consumes: %+v", doc.Nodes)
	}
	// (2-bis) «usa-los obriga a carimbar plan_version "1.2.0" (regra 6)»: o MESMO plano carimbado
	// com a linha que a FORMA MINIMA mostra é recusado. Sem o lembrete, a regra 12 empurrava o
	// modelo para `outputs`/`consumes` e a forma que ele copia dava uma recusa.
	if !strings.Contains(regra12, `plan_version "1.2.0" (regra 6)`) {
		t.Fatal("a regra 12 tem de lembrar que outputs/consumes obrigam a carimbar a linha 1.2.0")
	}
	if !strings.Contains(formaMinima(t), `"plan_version":"1.0.0"`) {
		t.Fatal("pre-condicao: a FORMA MINIMA deixou de mostrar 1.0.0; rever o lembrete da regra 12")
	}
	abaixo := strings.Replace(comContrato, `"plan_version":"1.2.0"`, `"plan_version":"1.0.0"`, 1)
	doc, err = plan.Decode([]byte(abaixo))
	if err != nil {
		t.Fatalf("o plano com contrato carimbado 1.0.0 nao passa o decode: %v", err)
	}
	if v := planvalidate.Validate(doc, snap, testCeilings()); !v.Rejected() || v.Reason != planvalidate.ReasonVersionBelowFeatures {
		t.Fatalf("outputs/consumes com plan_version 1.0.0 tinha de ser recusado com %s: %+v", planvalidate.ReasonVersionBelowFeatures, v)
	}
	// (3) A última frase da regra é verdadeira: a dependência só de ordem continua válida
	// (a opção (A) do ticket — recusá-la na validação — foi recusada pelo dono).
	soOrdem := strings.NewReplacer(contratoDoProdutor, "", contratoDoConsumidor, "",
		`"plan_version":"1.2.0"`, `"plan_version":"1.0.0"`).Replace(comContrato)
	doc, err = plan.Decode([]byte(soOrdem))
	if err != nil {
		t.Fatalf("o plano so de ordem nao passa o decode: %v", err)
	}
	if len(doc.Nodes[0].Outputs) != 0 || len(doc.Nodes[1].Consumes) != 0 || len(doc.Nodes[1].DependsOn) != 1 {
		t.Fatalf("pre-condicao: o mutante so de ordem tinha de ficar com depends_on e sem contrato: %+v", doc.Nodes)
	}
	if v := planvalidate.Validate(doc, snap, testCeilings()); !v.OK {
		t.Fatalf("depends_on sem consumes tem de continuar valido (AOS-484, opcao (A) recusada): %+v", v)
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
