package agentruntime

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"testing"

	"github.com/aos-ref/kernel/reference-monitor/taint"
)

// AOS-551, fatia 1b — o `tool_result` por HANDLE na 1.8.0.
//
// A segunda superfície por onde conteúdo de fora entrava no contexto do modelo. O que esta suite
// fixa: o output de uma tool permitida não chega ao prompt; o que chega é a referência, na forma
// que o resolvedor aceita; e um corpo que NÃO é referência materializa-se como erro de tool,
// nunca inline — porque na 1.8.0 o kernel exige, não fareja.

func resultadoDeTeste(ref string) CapturedToolResult {
	return CapturedToolResult{
		Invocation: ToolInvocation{ToolID: "doc_read", Input: []byte(`{"p":"x"}`)},
		Result:     Untrusted([]byte(ref)),
	}
}

// refDeResultado é a forma textual de um handle, como o nó a produz no selo do resultado
// ([activity.WithResultSealer], dentro do efeito do step-ledger).
var refDeResultado = HandleRefPrefix + MintHandleToken("run-551b", "sha256:bb", 1)

func turnoDa180(t *testing.T, results []CapturedToolResult) []TailSegment {
	t.Helper()
	g := Goal{RunID: "run-551b", Objective: "o objectivo"}
	_, seq, err := OpenRunTail(AssemblyVersion180, g)
	if err != nil {
		t.Fatalf("OpenRunTail: %v", err)
	}
	segs, _ := seq.Turn("step-000001", "", results)
	return segs
}

func segmentoDoTipo(segs []TailSegment, kind TailKind) *TailSegment {
	for i := range segs {
		if segs[i].Kind == kind {
			return &segs[i]
		}
	}
	return nil
}

// A afirmação central: o output da tool não chega ao prompt, e a referência chega.
func TestAOS551B_OOutputDaToolNaoEntraNoPrompt(t *testing.T) {
	// O nó selou: o corpo do resultado É a referência.
	segs := turnoDa180(t, []CapturedToolResult{resultadoDeTeste(refDeResultado)})
	seg := segmentoDoTipo(segs, TailToolResult)
	if seg == nil {
		t.Fatal("o turno não trouxe segmento tool_result")
	}
	if string(seg.Content) != corpoDeResultadoPorHandle180 {
		t.Fatalf("o corpo não é o fixo da 1.8.0:\n%q", seg.Content)
	}
	var ref string
	for _, m := range seg.Meta {
		if m.Key == ToolResultHandleLabel {
			ref = m.Value
		}
	}
	if ref != refDeResultado {
		t.Fatalf("o rótulo %s não trouxe a referência do nó: %q", ToolResultHandleLabel, ref)
	}
	// E a identidade da chamada sobrevive: sem ela o modelo não liga o resultado ao que pediu.
	var temID, temNome bool
	for _, m := range seg.Meta {
		switch m.Key {
		case "id":
			temID = true
		case "name":
			temNome = m.Value == "doc_read"
		}
	}
	if !temID || !temNome {
		t.Fatalf("a identidade da chamada perdeu-se nos rótulos: %+v", seg.Meta)
	}
}

// O que o tail oferece tem de ser o que o resolvedor aceita — lido DO PROMPT materializado, onde o
// saneamento do rótulo já correu. É o bloqueante que a fatia 1a teve, fechado aqui à cabeça.
func TestAOS551B_OQueOTailOfereceEOQueOResolvedorAceita(t *testing.T) {
	segs := turnoDa180(t, []CapturedToolResult{resultadoDeTeste(refDeResultado)})
	prompt := materializar(t, AssemblyVersion180, segs)

	i := strings.Index(prompt, ToolResultHandleLabel+"=")
	if i < 0 {
		t.Fatalf("o prompt não tem o rótulo %s:\n%s", ToolResultHandleLabel, prompt)
	}
	resto := prompt[i+len(ToolResultHandleLabel)+1:]
	fim := strings.IndexAny(resto, " >\n")
	if fim < 0 {
		t.Fatalf("o rótulo não fecha: %q", resto)
	}
	lido := resto[:fim]
	if _, ok := ParseHandleRef(lido); !ok {
		t.Fatalf("ParseHandleRef recusa o que o prompt oferece (%q): a call com este valor não resolve NEM é recusada", lido)
	}
	if !strings.Contains(corpoDeResultadoPorHandle180, HandleRefPrefix) {
		t.Fatalf("o corpo fixo não nomeia o prefixo %s, logo o modelo não sabe que forma escrever", HandleRefPrefix)
	}
}

// O KERNEL EXIGE, NÃO FAREJA. Um corpo que não é referência materializa-se como erro de tool, e
// nenhum byte dele entra no contexto. Se farejasse, uma tool forjava uma referência — e, pior, o
// output genuíno de uma tool num nó mal composto entrava inline sob um número que afirma o
// contrário.
func TestAOS551B_CorpoQueNaoEReferenciaViraErroNuncaInline(t *testing.T) {
	for _, corpo := range []string{
		"o conteudo verdadeiro da pagina",
		"h1.00112233445566",                       // o token nu: a forma que a v1 da fatia 1a produzia
		HandleRefPrefix,                           // prefixo sem token
		HandleRefPrefix + "h1.zz",                 // token mal formado
		"prefixo " + refDeResultado,               // referência com lixo à frente
		refDeResultado + " e mais conteudo",       // referência com lixo atrás
		"{\"handle\":\"" + refDeResultado + "\"}", // referência embrulhada: não é o contrato
	} {
		segs := turnoDa180(t, []CapturedToolResult{resultadoDeTeste(corpo)})
		seg := segmentoDoTipo(segs, TailToolResult)
		if seg == nil {
			t.Fatalf("corpo %q: sem segmento tool_result", corpo)
		}
		if bytes.Contains(seg.Content, []byte(corpo)) {
			t.Fatalf("corpo %q entrou INLINE no tail:\n%q", corpo, seg.Content)
		}
		if !bytes.Contains(seg.Content, []byte(ErrToolResultNotByHandle.Error())) {
			t.Fatalf("corpo %q não materializou o erro declarado:\n%q", corpo, seg.Content)
		}
		for _, m := range seg.Meta {
			if m.Key == ToolResultHandleLabel {
				t.Fatalf("corpo %q saiu com rótulo de handle (%q) sem ser uma referência", corpo, m.Value)
			}
			if m.Key == "tool_error" && m.Value != "1" {
				t.Fatalf("corpo %q: rótulo tool_error fora do vocabulário fechado: %q", corpo, m.Value)
			}
		}
	}
	// A mensagem do erro é FIXA e não leva nada do conteúdo.
	if strings.ContainsAny(ErrToolResultNotByHandle.Error(), "\n<\\") {
		t.Errorf("a mensagem do erro tem bytes que a neutralização alteraria: %q", ErrToolResultNotByHandle.Error())
	}
}

// As negações e os erros de tool NÃO mudam: não têm conteúdo devolvido. Um permit com output vazio
// também não tem nada para selar. Nos três casos a 1.8.0 materializa os bytes da 1.7.0.
func TestAOS551B_NegacaoErroEVazioMaterializamOsBytesDa170(t *testing.T) {
	casos := map[string]CapturedToolResult{
		"negada": {
			Invocation: ToolInvocation{ToolID: "web_post"},
			Result:     Untrusted(nil),
			Denial:     &ToolDenial{Effect: "deny", Code: "E_POLICY", DeniedBy: "pdp"},
		},
		"erro de tool": {
			Invocation: ToolInvocation{ToolID: "doc_read"},
			Result:     Untrusted(nil),
			ToolError:  errors.New("ficheiro inexistente"),
		},
		"permit sem output": {
			Invocation: ToolInvocation{ToolID: "doc_read"},
			Result:     Untrusted(nil),
		},
	}
	for nome, c := range casos {
		s180 := turnoDa180(t, []CapturedToolResult{c})

		g := Goal{RunID: "run-551b", Objective: "o objectivo"}
		_, seq170, err := OpenRunTail(AssemblyVersion170, g)
		if err != nil {
			t.Fatalf("%s: 1.7.0: %v", nome, err)
		}
		s170, _ := seq170.Turn("step-000001", "", []CapturedToolResult{c})

		a := materializar(t, AssemblyVersion170, s170)
		b := materializar(t, AssemblyVersion180, s180)
		if a != b {
			t.Fatalf("%s: a 1.8.0 divergiu da 1.7.0:\n1.7.0:\n%s\n1.8.0:\n%s", nome, a, b)
		}
	}
}

// Um turno MISTO — uma call selada, uma negada e uma com erro — mantém cada uma na sua forma, pela
// ordem. É o caso que a autoridade por call do replay vai ter de reproduzir (fatia 3), e também o
// que mostra que a escolha é por RESULTADO e não por turno.
func TestAOS551B_TurnoMistoMantemCadaResultadoNaSuaForma(t *testing.T) {
	segs := turnoDa180(t, []CapturedToolResult{
		resultadoDeTeste(refDeResultado),
		{Invocation: ToolInvocation{ToolID: "web_post"}, Result: Untrusted(nil), Denial: &ToolDenial{Effect: "deny"}},
		{Invocation: ToolInvocation{ToolID: "doc_read"}, Result: Untrusted([]byte("conteudo cru")), ToolError: errors.New("falhou")},
	})
	var resultados []TailSegment
	for _, s := range segs {
		if s.Kind == TailToolResult {
			resultados = append(resultados, s)
		}
	}
	if len(resultados) != 3 {
		t.Fatalf("queria 3 tool_result, veio %d", len(resultados))
	}
	if string(resultados[0].Content) != corpoDeResultadoPorHandle180 {
		t.Error("o primeiro resultado (selado) não ficou por handle")
	}
	if len(resultados[1].Content) != 0 {
		t.Errorf("o resultado negado ganhou corpo: %q", resultados[1].Content)
	}
	// O terceiro tem erro de tool: a mensagem do erro fica (é diagnóstico), mas o OUTPUT CRU NÃO.
	// Era o defeito da primeira versão desta fatia: o ramo de sempre recebia o `r` inteiro e
	// [tailResultado] acrescentava o valor ao corpo — o output cru no contexto do modelo, na
	// própria versão que existe para o tirar de lá.
	if !bytes.Contains(resultados[2].Content, []byte("falhou")) {
		t.Errorf("o resultado com erro perdeu a mensagem do erro: %q", resultados[2].Content)
	}
	if bytes.Contains(resultados[2].Content, []byte("conteudo cru")) {
		t.Fatalf("o output CRU entrou inline pelo ramo do tool_error: %q", resultados[2].Content)
	}
}

// NUM LAYOUT POR HANDLE O VALOR NUNCA CHEGA AO CORPO, em nenhum ramo. É a garantia ESTRUTURAL que
// o commit do `plan_input` estabeleceu como regra («não uma promessa sobre os chamadores de hoje»)
// e que a primeira versão desta fatia não cumpria na superfície que acabou de abrir.
func TestAOS551B_EmNenhumRamoOValorChegaAoCorpo(t *testing.T) {
	const cru = "O CONTEUDO QUE NAO PODE APARECER"
	casos := map[string]CapturedToolResult{
		"permit com valor que nao e referencia": {
			Invocation: ToolInvocation{ToolID: "doc_read"}, Result: Untrusted([]byte(cru)),
		},
		"erro de tool com valor": {
			Invocation: ToolInvocation{ToolID: "doc_read"}, Result: Untrusted([]byte(cru)),
			ToolError: errors.New("falhou a meio"),
		},
		"negada com valor": {
			Invocation: ToolInvocation{ToolID: "web_post"}, Result: Untrusted([]byte(cru)),
			Denial: &ToolDenial{Effect: "deny", Code: "E_POLICY"},
		},
	}
	for nome, c := range casos {
		segs := turnoDa180(t, []CapturedToolResult{c})
		prompt := materializar(t, AssemblyVersion180, segs)
		if strings.Contains(prompt, cru) {
			t.Errorf("%s: o valor cru entrou nos bytes que o modelo recebe:\n%s", nome, prompt)
		}
	}
}

// A `media_view` É A EXCEPÇÃO ao caminho dos handles, e por desenho da fase.
//
// A 1.8.0 herda os segmentos de media da 1.7.0, e `mediaDoTurno` lê os resultados CRUS — não os
// segmentos materializados. A primeira versão desta fatia tinha a colisão nos DOIS sentidos:
//
//   - sem o PRODUTOR composto, o `tool_result` da `media_view` materializava
//     «tool result not available by handle» porque a `MediaRef` não é uma referência de handle, E
//     o segmento de media era acrescentado a seguir — o prompt dizia ao modelo que a chamada não
//     deu resultado utilizável e anexava-lhe a imagem;
//   - com o envelope a selar, `parseMediaRef` falharia sobre uma referência nua e a imagem deixava
//     SILENCIOSAMENTE de chegar ao modelo: regressão da R1a.
//
// Não é inconsistência da R2. A correcção 1 do AOS-551 diz que a media é PROJECTADA para o modelo
// e que é por isso que a autoridade de uma call tem de a contar; o que fica inline é a `MediaRef`
// (doc, digest, tipo, bytes) que o NÓ produziu, não os bytes da imagem.
func TestAOS551B_AMediaViewFicaNoMoldeDa170(t *testing.T) {
	ref := MediaRef{Doc: "doc://planta", SHA256: "sha256:" + strings.Repeat("ab", 32), MediaType: "image/png", Bytes: 1024}
	corpo, err := ref.Marshal()
	if err != nil {
		t.Fatalf("MediaRef.Marshal: %v", err)
	}
	c := CapturedToolResult{Invocation: ToolInvocation{ToolID: MediaViewTool}, Result: Untrusted(corpo)}

	s180 := turnoDa180(t, []CapturedToolResult{c})

	g := Goal{RunID: "run-551b", Objective: "o objectivo"}
	_, seq170, err := OpenRunTail(AssemblyVersion170, g)
	if err != nil {
		t.Fatalf("1.7.0: %v", err)
	}
	s170, _ := seq170.Turn("step-000001", "", []CapturedToolResult{c})

	if a, b := materializar(t, AssemblyVersion170, s170), materializar(t, AssemblyVersion180, s180); a != b {
		t.Fatalf("a media_view divergiu da 1.7.0 na 1.8.0:\n1.7.0:\n%s\n1.8.0:\n%s", a, b)
	}
	res := segmentoDoTipo(s180, TailToolResult)
	if res == nil {
		t.Fatal("sem segmento tool_result")
	}
	if bytes.Contains(res.Content, []byte(ErrToolResultNotByHandle.Error())) {
		t.Error("o tail diz ao modelo que a media_view não deu resultado utilizável, e a seguir anexa-lhe a imagem")
	}
	if segmentoDoTipo(s180, TailMedia) == nil {
		t.Error("a 1.8.0 deixou de acrescentar o segmento de media — regressão da R1a")
	}
}

// A pergunta de quem compõe o nó, e o seu vocabulário fechado.
func TestAOS551B_HasToolResultsByHandle(t *testing.T) {
	if !HasToolResultsByHandle(AssemblyVersion180) {
		t.Error("a 1.8.0 não põe os tool_result por handle")
	}
	for _, v := range []string{AssemblyVersion130, AssemblyVersion140, AssemblyVersion150, AssemblyVersion160, AssemblyVersion170, "", "1.9.0"} {
		if HasToolResultsByHandle(v) {
			t.Errorf("HasToolResultsByHandle(%q) = true", v)
		}
	}
}

// O corpo fixo do resultado cumpre as mesmas restrições do da entrada — é a mesma classe de
// constante, lida no mesmo sítio pelo mesmo modelo.
func TestAOS551B_OCorpoFixoDoResultadoCumpreAsRestricoes(t *testing.T) {
	corpo := corpoDeResultadoPorHandle180
	for _, linha := range strings.Split(strings.TrimSuffix(corpo, "\n"), "\n") {
		if strings.HasPrefix(linha, "<") || strings.HasPrefix(linha, "\\") {
			t.Errorf("linha a abrir por '<' ou '\\' muda os bytes na neutralização: %q", linha)
		}
	}
	for _, rotulo := range []string{
		"taint", "tool_denied", "denied_code", "denied_by", "tool_error", "id", "name", "about",
		"ref", "args_digest", "args_omitted_bytes", StateDigestLabel, PlanInputHandleLabel,
		ToolResultHandleLabel, "plan_input_from", "plan_input_output", "plan_input_digest",
		// Os quatro da media da 1.7.0, que a 1.8.0 herda e que a lista do `plan_input` omitia.
		"media_doc", "media_sha256", "media_type", "media_bytes",
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
	// E não inverte a ordem da mediação, pela razão do corpo da entrada.
	baixo := strings.ToLower(corpo)
	if strings.Contains(baixo, "after the call has been authorized") {
		t.Fatal("o corpo diz que a substituição é DEPOIS da autorização; é antes")
	}
	if !strings.Contains(baixo, "before the call is authorized") {
		t.Fatal("o corpo não afirma que a substituição é ANTES da autorização")
	}
}

// NUM LAYOUT POR HANDLE, SÓ UM RESULTADO JÁ SELADO SE DESIGNA — e é a forma do VALOR que decide,
// não o layout.
//
// A fatia 1b recusava por LAYOUT, porque não havia produtor e nenhum resultado era uma referência.
// Com o produtor ([activity.WithResultSealer]) há dois casos a distinguir, e o layout não os
// distingue:
//
//   - o valor É a referência (o nó composto com o selo): designa, e o digest descreve exactamente
//     os bytes que o step-ledger gravou — que são os mesmos que os dois verificadores da âncora
//     re-hasheiam. A âncora confere;
//   - o valor é conteúdo CRU (o nó que compôs a 1.8.0 sem o selo): NÃO designa. Recusar por layout
//     também o cobria, mas recusar por forma cobre-o sem depender de ninguém se lembrar de compor
//     o selo — e era este o caso fail-OPEN: o tail esconde o conteúdo do modelo (materializa-o
//     como erro de tool) mas a designação digeria-o e o `aos-orq` entregá-lo-ia a jusante como
//     saída do run.
func TestAOS551B_SoUmResultadoSeladoSeDesigna(t *testing.T) {
	designarCom := func(porHandle bool, valor string) *OutputSource {
		t.Helper()
		e := NewRunEvidence()
		e.FollowOutputFrom("doc_read")
		e.ResultsByHandle(porHandle)
		e.Observe("step-000001", taint.Trusted, []CapturedToolResult{{
			Invocation: ToolInvocation{ToolID: "doc_read"},
			Result:     Untrusted([]byte(valor)),
		}})
		src, err := e.designar("doc_read", OutputSourceBinds)
		if err != nil {
			t.Fatalf("designar(%q): %v", valor, err)
		}
		return src
	}
	designar := func(valor string) *OutputSource { return designarCom(false, valor) }
	designarPorHandle := func(valor string) *OutputSource { return designarCom(true, valor) }

	// Conteúdo normal (os layouts até à 1.7.0): designa, com digest do conteúdo.
	normal := designar("o conteudo verdadeiro")
	if !normal.BemFormada() {
		t.Fatalf("um resultado de conteúdo deixou de se designar — regressão nos layouts em produção: %+v", normal)
	}
	if normal.Digest == "" || normal.Bytes == 0 {
		t.Fatalf("a designação de conteúdo perdeu o digest: %+v", normal)
	}

	// Num layout POR HANDLE com o valor JÁ SELADO: designa, e o digest é o dos bytes que o
	// despacho devolveu — a referência. É o que o step-ledger grava e o que os verificadores da
	// âncora re-hasheiam, pelo que a âncora confere. Um digest do CONTEÚDO seria inconfirmável
	// pelos dois e o run fecharia `fulfilled` com a saída designada indisponível de vez.
	porHandle := designarPorHandle(refDeResultado)
	if porHandle.State != OutputSourceDesignated {
		t.Fatalf("um resultado SELADO não designou: %+v", porHandle)
	}
	if !porHandle.BemFormada() {
		t.Fatalf("a âncora NÃO é selável — a transição terminal seria recusada e o run ficava sem desfecho: %+v", porHandle)
	}
	soma := sha256.Sum256([]byte(refDeResultado))
	if quero := "sha256:" + hex.EncodeToString(soma[:]); porHandle.Digest != quero {
		t.Fatalf("a âncora não atesta os bytes que o despacho devolveu: digest=%q, sha256(referência)=%q", porHandle.Digest, quero)
	}
	if porHandle.Bytes != len(refDeResultado) {
		t.Fatalf("a âncora conta %d bytes e a referência tem %d — os dois verificadores comparam o tamanho", porHandle.Bytes, len(refDeResultado))
	}

	// E NÃO designa quando o valor não é uma referência — o nó que compôs a 1.8.0 SEM o selo.
	// Fail-OPEN se designasse: o tail esconde esse conteúdo do modelo (materializa-o como erro de
	// tool) mas a designação digeri-lo-ia e o `aos-orq` entregá-lo-ia a jusante como saída do run,
	// conteúdo que o modelo nunca viu.
	semSelo := designarPorHandle("o conteudo cru que o no nao selou")
	if semSelo.State != OutputSourceMissing || !semSelo.BemFormada() {
		t.Fatalf("o nó sem o selo designou a origem a partir de conteúdo cru: %+v", semSelo)
	}
	if semSelo.Digest != "" {
		t.Fatalf("a designação digeriu o conteúdo cru: %+v", semSelo)
	}

	// E uma referência QUASE bem formada é conteúdo cru para este efeito: é o mesmo vocabulário de
	// [ParseHandleRef] que o tail usa, pelo que o que o tail recusa mostrar a designação recusa
	// atestar. Sem isto, lixo à volta de um token passava a âncora.
	for _, quase := range []string{
		"prefixo " + refDeResultado,
		refDeResultado + " e mais conteudo",
		`{"handle":"` + refDeResultado + `"}`,
		HandleRefPrefix,
	} {
		if s := designarPorHandle(quase); s.State != OutputSourceMissing || s.Digest != "" {
			t.Fatalf("%q designou a origem: %+v", quase, s)
		}
	}
}
