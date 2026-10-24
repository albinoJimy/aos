package agentruntime

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

// AOS-551, fase R2 do ADR-043 — o layout 1.8.0 e o `plan_input` POR HANDLE.
//
// O que esta suite fixa é a afirmação que dá sentido à fase: na 1.8.0 o payload de uma entrada do
// plano NÃO está no contexto do modelo. E fixa as duas propriedades sem as quais isso não serve de
// nada: que os layouts anteriores não mudam um byte, e que a 1.8.0 RECUSA em vez de cair no corpo
// inline quando o handle falta.

// materializar é os BYTES de uma sequência de segmentos no layout dado — o que o modelo recebe.
func materializar(t *testing.T, versao string, segs []TailSegment) string {
	t.Helper()
	var b []byte
	for _, seg := range segs {
		m, err := RenderTailSegment(versao, seg)
		if err != nil {
			t.Fatalf("RenderTailSegment(%s): %v", versao, err)
		}
		b = append(b, m...)
	}
	return string(b)
}

func entradaDeTeste(handle string) PlanInput {
	return PlanInput{From: "n1", Output: "doc", Digest: "sha256:aa", Content: []byte("segredo do no 1"), Handle: handle}
}

func goalComEntrada(versao, handle string) Goal {
	return Goal{RunID: "run-551", Objective: "o objectivo", Inputs: []PlanInput{entradaDeTeste(handle)}}
}

// A afirmação central: o payload não chega ao prompt, e o handle chega.
func TestAOS551_OPayloadNaoEntraNoTailDa180(t *testing.T) {
	segs, seq, err := OpenRunTail(AssemblyVersion180, goalComEntrada(AssemblyVersion180, "h1.00112233445566"))
	if err != nil {
		t.Fatalf("OpenRunTail: %v", err)
	}
	// A 1.8.0 herda o adiamento da 1.6.0: a entrada chega no turno seguinte, não na semente.
	entregues, _ := seq.Turn("s1", "ok", nil)
	var entrada *TailSegment
	for i := range entregues {
		if entregues[i].Kind == TailPlanInput {
			entrada = &entregues[i]
		}
	}
	if entrada == nil {
		t.Fatal("a entrega do turno 1 não trouxe segmento plan_input")
	}
	if bytes.Contains(entrada.Content, []byte("segredo do no 1")) {
		t.Fatalf("o PAYLOAD entrou no corpo do segmento — a separação de planos não existe: %q", entrada.Content)
	}
	if string(entrada.Content) != corpoDeEntradaPorHandle180 {
		t.Fatalf("o corpo não é o fixo da 1.8.0:\n%q", entrada.Content)
	}
	var token string
	for _, m := range entrada.Meta {
		if m.Key == PlanInputHandleLabel {
			token = m.Value
		}
	}
	if token != "h1.00112233445566" {
		t.Fatalf("o rótulo %s não trouxe o token do nó: %q", PlanInputHandleLabel, token)
	}
	// E o payload também não entra pela semente, onde entrava até à 1.5.0.
	for _, s := range segs {
		if bytes.Contains(s.Content, []byte("segredo do no 1")) {
			t.Fatalf("o payload entrou na SEMENTE (kind %v)", s.Kind)
		}
	}
}

// O payload não entra no prompt MATERIALIZADO — a asserção sobre os bytes que o modelo recebe, e
// não sobre a estrutura que os produz.
func TestAOS551_OPayloadNaoEntraNoPromptMaterializado(t *testing.T) {
	_, seq, err := OpenRunTail(AssemblyVersion180, goalComEntrada(AssemblyVersion180, "h1.00112233445566"))
	if err != nil {
		t.Fatalf("OpenRunTail: %v", err)
	}
	entregues, _ := seq.Turn("s1", "ok", nil)
	prompt := materializar(t, AssemblyVersion180, entregues)
	if strings.Contains(prompt, "segredo do no 1") {
		t.Fatal("o payload está nos bytes que o modelo recebe")
	}
	if !strings.Contains(prompt, "h1.00112233445566") {
		t.Fatal("o token do handle NÃO está nos bytes que o modelo recebe — ele não tem como pedir o conteúdo")
	}
}

// FAIL-CLOSED: sem handle, a 1.8.0 recusa. Não cai no corpo inline, que seria a 1.7.0 a correr com
// o número da 1.8.0 — a separação desfeita em silêncio por baixo de uma versão que a afirma.
func TestAOS551_SemHandleA180Recusa(t *testing.T) {
	_, _, err := OpenRunTail(AssemblyVersion180, goalComEntrada(AssemblyVersion180, ""))
	if !errors.Is(err, ErrPlanInputWithoutHandle) {
		t.Fatalf("esperado ErrPlanInputWithoutHandle, veio %v", err)
	}
	// E o erro nomeia a entrada, porque num run com várias o operador tem de saber qual.
	if !strings.Contains(err.Error(), "n1>doc") {
		t.Fatalf("o erro não nomeia a entrada: %v", err)
	}
}

// Um run SEM entradas materializa na 1.8.0 os mesmos bytes que na 1.7.0 — a propriedade que torna
// a versão nova adoptável sem reexplicar nenhum `prompt_hash` existente.
func TestAOS551_SemEntradasA180EByteIdenticaA170(t *testing.T) {
	g := Goal{RunID: "run-551", Objective: "o objectivo"}
	s170, q170, err := OpenRunTail(AssemblyVersion170, g)
	if err != nil {
		t.Fatalf("1.7.0: %v", err)
	}
	s180, q180, err := OpenRunTail(AssemblyVersion180, g)
	if err != nil {
		t.Fatalf("1.8.0: %v", err)
	}
	p170 := materializar(t, AssemblyVersion170, s170)
	p180 := materializar(t, AssemblyVersion180, s180)
	if p170 != p180 {
		t.Fatalf("a semente divergiu num run sem entradas:\n1.7.0:\n%s\n1.8.0:\n%s", p170, p180)
	}
	t170, _ := q170.Turn("s1", "texto", nil)
	t180, _ := q180.Turn("s1", "texto", nil)
	if len(t170) != len(t180) {
		t.Fatalf("o turno divergiu: %d vs %d segmentos", len(t170), len(t180))
	}
	for i := range t170 {
		a := materializar(t, AssemblyVersion170, t170[i:i+1])
		b := materializar(t, AssemblyVersion180, t180[i:i+1])
		if a != b {
			t.Fatalf("o segmento %d divergiu:\n%s\n%s", i, a, b)
		}
	}
}

// A 1.8.0 é CUMULATIVA: herda o adiamento da 1.6.0 e os segmentos de media da 1.7.0. O segundo é o
// que impede a leitura errada de «o modelo que decide deixa de ver untrusted» — a media continua
// projectada para ele, e é por isso que a autoridade de uma call tem de a contar.
func TestAOS551_A180HerdaOAdiamentoEAMedia(t *testing.T) {
	if !DefersPlanInputs(AssemblyVersion180) {
		t.Error("a 1.8.0 não adia os plan_input — não deriva da 1.6.0")
	}
	if !HasMediaSegments(AssemblyVersion180) {
		t.Error("a 1.8.0 não tem segmentos de media — não deriva da 1.7.0, e a premissa da autoridade cai")
	}
	if !HasPlanInputsByHandle(AssemblyVersion180) {
		t.Error("HasPlanInputsByHandle diz que a 1.8.0 não põe as entradas por handle")
	}
	for _, v := range []string{AssemblyVersion130, AssemblyVersion140, AssemblyVersion150, AssemblyVersion160, AssemblyVersion170, "", "9.9.9"} {
		if HasPlanInputsByHandle(v) {
			t.Errorf("HasPlanInputsByHandle(%q) = true", v)
		}
	}
}

// A 1.8.0 entra no vocabulário FECHADO de quem rotula por layout, e não é o layout dos runs novos:
// o interruptor não existe, por desenho (a autoridade por argumento e a política são trabalho a
// seguir).
func TestAOS551_A180NoVocabularioEForaDosRunsNovos(t *testing.T) {
	var tem bool
	for _, v := range SupportedAssemblyVersions() {
		if v == AssemblyVersion180 {
			tem = true
		}
	}
	if !tem {
		t.Error("a 1.8.0 não está em SupportedAssemblyVersions — a métrica por layout não a saberia rotular")
	}
	if AssemblyVersion == AssemblyVersion180 {
		t.Error("a 1.8.0 é o layout dos runs novos — não pode ser: sem a autoridade por argumento, a separação troca «negar a mais» por «permitir a mais»")
	}
	if err := ValidateAssemblyVersion(AssemblyVersion180); err != nil {
		t.Errorf("o gate de admissão do replay recusa a 1.8.0: %v", err)
	}
}

// O corpo fixo cumpre as restrições do preâmbulo da 1.4.0, pela mesma razão: vive num sítio onde
// o modelo procura rótulos estruturais, e não pode responder por eles.
func TestAOS551_OCorpoFixoCumpreAsRestricoesDoPreambulo(t *testing.T) {
	corpo := corpoDeEntradaPorHandle180
	for _, linha := range strings.Split(strings.TrimSuffix(corpo, "\n"), "\n") {
		if strings.HasPrefix(linha, "<") {
			t.Errorf("linha a abrir por '<' seria lida como delimitador genuíno: %q", linha)
		}
		if strings.HasPrefix(linha, "\\") {
			t.Errorf("linha a abrir por '\\' passa pela neutralização e muda os bytes: %q", linha)
		}
	}
	if strings.Contains(corpo, "taint=") {
		t.Error("o corpo contém um rótulo de confiança")
	}
	if strings.Contains(corpo, "tool_denied=") || strings.Contains(corpo, PlanInputHandleLabel+"=") {
		t.Error("o corpo escreve um rótulo genuíno seguido de '=' — indistinguível do que o runtime afirma")
	}
	for i := 0; i < len(corpo); i++ {
		if corpo[i] > 0x7e || (corpo[i] < 0x20 && corpo[i] != '\n') {
			t.Fatalf("byte não-ASCII-imprimível na posição %d: %q", i, corpo[i])
		}
	}
}

// O digest das entradas adiadas — o que o `turn.recorded` do turno que NÃO as teve grava — passa a
// ser o digest dos segmentos POR HANDLE. Senão, o número gravado descreveria um tail que nunca
// existiu neste run.
func TestAOS551_ODigestDasAdiadasEODaFormaQueOLayoutMaterializa(t *testing.T) {
	g := goalComEntrada(AssemblyVersion180, "h1.00112233445566")
	_, q180, err := OpenRunTail(AssemblyVersion180, g)
	if err != nil {
		t.Fatalf("1.8.0: %v", err)
	}
	g170 := g
	g170.Inputs = []PlanInput{entradaDeTeste("")}
	_, q170, err := OpenRunTail(AssemblyVersion170, g170)
	if err != nil {
		t.Fatalf("1.7.0: %v", err)
	}
	if q180.DeferredPlanInputsDigest() == q170.DeferredPlanInputsDigest() {
		t.Fatal("o digest das adiadas é igual nos dois layouts — está a ser calculado sobre o payload, não sobre o que a 1.8.0 materializa")
	}
	if q180.DeferredPlanInputsDigest() == "" {
		t.Fatal("a 1.8.0 não gravou digest das adiadas")
	}
}
