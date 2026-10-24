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

// handleDeTeste é a forma TEXTUAL de um handle, como o nó a produz ([Handle.Ref]).
var handleDeTeste = HandleRefPrefix + MintHandleToken("run-551", "sha256:aa", 1)

func entradaDeTeste(handle string) PlanInput {
	return PlanInput{From: "n1", Output: "doc", Digest: "sha256:aa", Content: []byte("segredo do no 1"), Handle: handle}
}

func goalComEntrada(handle string) Goal {
	return Goal{RunID: "run-551", Objective: "o objectivo", Inputs: []PlanInput{entradaDeTeste(handle)}}
}

// A afirmação central: o payload não chega ao prompt, e o handle chega.
func TestAOS551_OPayloadNaoEntraNoTailDa180(t *testing.T) {
	segs, seq, err := OpenRunTail(AssemblyVersion180, goalComEntrada(handleDeTeste))
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
	if token != handleDeTeste {
		t.Fatalf("o rótulo %s não trouxe a referência do nó: %q", PlanInputHandleLabel, token)
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
	_, seq, err := OpenRunTail(AssemblyVersion180, goalComEntrada(handleDeTeste))
	if err != nil {
		t.Fatalf("OpenRunTail: %v", err)
	}
	entregues, _ := seq.Turn("s1", "ok", nil)
	prompt := materializar(t, AssemblyVersion180, entregues)
	if strings.Contains(prompt, "segredo do no 1") {
		t.Fatal("o payload está nos bytes que o modelo recebe")
	}
	if !strings.Contains(prompt, handleDeTeste) {
		t.Fatal("a referência do handle NÃO está nos bytes que o modelo recebe — ele não tem como pedir o conteúdo")
	}
}

// O BLOQUEANTE da primeira versão, fechado em teste: o que o tail OFERECE ao modelo tem de ser
// exactamente o que o resolvedor ACEITA. A primeira versão punha no rótulo o token nu; ele não era
// resolvido ([ParseHandleRef] exige o prefixo) e também não era recusado — o `CallRewriter` só
// recusa um argumento com o prefixo mal formado —, pelo que seguia para o Reference Monitor como
// conteúdo literal, autorizado e selado sobre a string do token.
//
// Este teste lê o valor DO PROMPT MATERIALIZADO, e não da estrutura: é no prompt que o saneamento
// do rótulo já correu, e é a string do prompt que o modelo copia.
func TestAOS551_OQueOTailOfereceEOQueOResolvedorAceita(t *testing.T) {
	_, seq, err := OpenRunTail(AssemblyVersion180, goalComEntrada(handleDeTeste))
	if err != nil {
		t.Fatalf("OpenRunTail: %v", err)
	}
	entregues, _ := seq.Turn("s1", "ok", nil)
	prompt := materializar(t, AssemblyVersion180, entregues)

	// Extrai o valor do rótulo tal como o modelo o lê, do prompt.
	i := strings.Index(prompt, PlanInputHandleLabel+"=")
	if i < 0 {
		t.Fatalf("o prompt não tem o rótulo %s:\n%s", PlanInputHandleLabel, prompt)
	}
	resto := prompt[i+len(PlanInputHandleLabel)+1:]
	fim := strings.IndexAny(resto, " >\n")
	if fim < 0 {
		t.Fatalf("o rótulo não fecha: %q", resto)
	}
	lido := resto[:fim]

	if _, ok := ParseHandleRef(lido); !ok {
		t.Fatalf("ParseHandleRef recusa o que o prompt oferece ao modelo (%q): a call com este valor não resolve NEM é recusada — passa ao RM como literal", lido)
	}
	if !strings.HasPrefix(lido, HandleRefPrefix) {
		t.Fatalf("o valor no prompt não leva %s: %q", HandleRefPrefix, lido)
	}
	// E o corpo manda o modelo escrevê-lo COM o prefixo, senão a instrução contradiz o resolvedor.
	if !strings.Contains(corpoDeEntradaPorHandle180, HandleRefPrefix) {
		t.Fatalf("o corpo fixo não nomeia o prefixo %s, logo o modelo não sabe que forma escrever", HandleRefPrefix)
	}
}

// O corpo fixo não pode afirmar o contrário do mecanismo. O AOS-550 resolve o handle NO
// `CallRewriter`, ANTES da mediação — é por isso que o Reference Monitor decide sobre os BYTES, e é
// essa a propriedade que dá valor à fase. A primeira versão dizia «after the call has been
// authorized», que é exactamente o inverso, numa constante versionada que o modelo lê.
func TestAOS551_OCorpoFixoNaoInverteAOrdemDaMediacao(t *testing.T) {
	corpo := strings.ToLower(corpoDeEntradaPorHandle180)
	if strings.Contains(corpo, "after the call has been authorized") || strings.Contains(corpo, "after it is authorized") {
		t.Fatal("o corpo diz que a substituição é DEPOIS da autorização; é antes")
	}
	if !strings.Contains(corpo, "before the call is authorized") {
		t.Fatal("o corpo não afirma que a substituição é ANTES da autorização — a propriedade que o AOS-550 garante e de que o AOS-556 vai depender")
	}
}

// FAIL-CLOSED: sem handle, a 1.8.0 recusa. Não cai no corpo inline, que seria a 1.7.0 a correr com
// o número da 1.8.0 — a separação desfeita em silêncio por baixo de uma versão que a afirma.
func TestAOS551_SemHandleA180Recusa(t *testing.T) {
	_, _, err := OpenRunTail(AssemblyVersion180, goalComEntrada(""))
	if !errors.Is(err, ErrPlanInputWithoutHandle) {
		t.Fatalf("esperado ErrPlanInputWithoutHandle, veio %v", err)
	}
	// E o erro nomeia a entrada, porque num run com várias o operador tem de saber qual.
	if !strings.Contains(err.Error(), "n1>doc") {
		t.Fatalf("o erro não nomeia a entrada: %v", err)
	}
	// E recusa também o MAL FORMADO, não só o ausente: uma referência que não resolve ofereceria ao
	// modelo um handle que o `CallRewriter` não substitui. O saneamento do rótulo torna isto
	// alcançável sem malícia — `h1.aa bb` materializa `h1.aa_bb`, que nunca resolverá.
	for _, mau := range []string{"h1.00112233445566", "x", "aos-handle/v1:", "aos-handle/v1:h1.zz", "aos-handle/v2:h1.00112233445566", HandleRefPrefix + "h1.aa bb", " " + handleDeTeste} {
		if _, _, err := OpenRunTail(AssemblyVersion180, goalComEntrada(mau)); !errors.Is(err, ErrPlanInputWithoutHandle) {
			t.Errorf("handle %q aceito (err=%v): o tail ofereceria ao modelo algo que não resolve", mau, err)
		}
	}
	// O bem formado passa — senão este teste passava com o gate a recusar tudo.
	if _, _, err := OpenRunTail(AssemblyVersion180, goalComEntrada(handleDeTeste)); err != nil {
		t.Fatalf("a referência bem formada foi recusada: %v", err)
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
	// TODOS os rótulos genuínos, e não só dois: um `=` a seguir a qualquer um deles no corpo é
	// indistinguível do que o runtime afirma na linha de delimitação.
	for _, rotulo := range []string{
		"taint", "tool_denied", "denied_code", "denied_by", "tool_error", "id", "name", "about",
		"ref", "args_digest", "args_omitted_bytes", StateDigestLabel, PlanInputHandleLabel,
		"plan_input_from", "plan_input_output", "plan_input_digest",
	} {
		if strings.Contains(corpo, rotulo+"=") {
			t.Errorf("o corpo escreve o rótulo genuíno %q seguido de '='", rotulo)
		}
	}
	for i := 0; i < len(corpo); i++ {
		if corpo[i] > 0x7e || (corpo[i] < 0x20 && corpo[i] != '\n') {
			t.Fatalf("byte não-ASCII-imprimível na posição %d: %q", i, corpo[i])
		}
	}
}

// O digest das entradas adiadas descreve AS ENTRADAS, não o tail — e por isso tem de ser
// INDEPENDENTE DO RUN. É o número que o `turn.recorded` grava no turno que não as teve, e o único
// consumidor dele compara uma tentativa com o run anterior (AOS-502/506): entradas byte-idênticas
// em duas tentativas têm de dar o mesmo número, ou o sinal deixa de distinguir «as entradas
// mudaram» de «é outra tentativa».
//
// Foi a GRAVE da revisão adversarial: a primeira versão calculava-o sobre a forma que o layout
// materializa, que na 1.8.0 inclui o rótulo do handle — e o token é escopado ao run por desenho.
// O sinal passava a disparar sempre.
func TestAOS551_ODigestDasAdiadasEIndependenteDoRun(t *testing.T) {
	digestDe := func(runID, handle string) string {
		g := goalComEntrada(handle)
		g.RunID = runID
		_, q, err := OpenRunTail(AssemblyVersion180, g)
		if err != nil {
			t.Fatalf("OpenRunTail(%s): %v", runID, err)
		}
		return q.DeferredPlanInputsDigest()
	}
	// Mesmas entradas, DOIS runs — portanto dois handles diferentes, porque o token é escopado ao
	// run. O digest tem de ser o mesmo.
	a := digestDe("run-tentativa-1", HandleRefPrefix+MintHandleToken("run-tentativa-1", "sha256:aa", 1))
	b := digestDe("run-tentativa-2", HandleRefPrefix+MintHandleToken("run-tentativa-2", "sha256:aa", 1))
	if a == "" {
		t.Fatal("a 1.8.0 não gravou digest das adiadas")
	}
	if a != b {
		t.Fatalf("o digest depende do run — o sinal de AOS-506 dispararia em toda a tentativa:\n%s\n%s", a, b)
	}

	// E é COMPARÁVEL com os layouts anteriores, que é o que quem compara tentativas precisa quando
	// o nó muda de layout entre elas.
	g170 := goalComEntrada("")
	g170.RunID = "run-tentativa-1"
	_, q170, err := OpenRunTail(AssemblyVersion170, g170)
	if err != nil {
		t.Fatalf("1.7.0: %v", err)
	}
	if q170.DeferredPlanInputsDigest() != a {
		t.Fatalf("o digest da 1.8.0 não é comparável com o da 1.7.0 para as mesmas entradas:\n1.7.0 %s\n1.8.0 %s", q170.DeferredPlanInputsDigest(), a)
	}

	// Controlo: entradas DIFERENTES dão números diferentes, senão o teste acima passaria com um
	// digest constante.
	g2 := goalComEntrada(handleDeTeste)
	g2.RunID = "run-tentativa-1"
	g2.Inputs = []PlanInput{{From: "n1", Output: "doc", Digest: "sha256:bb", Content: []byte("outro conteudo"), Handle: handleDeTeste}}
	_, q2, err := OpenRunTail(AssemblyVersion180, g2)
	if err != nil {
		t.Fatalf("entradas diferentes: %v", err)
	}
	if q2.DeferredPlanInputsDigest() == a {
		t.Fatal("entradas diferentes dão o mesmo digest — o número não mede nada")
	}
}
