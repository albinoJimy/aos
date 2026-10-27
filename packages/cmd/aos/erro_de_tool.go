package main

// O VOCABULÁRIO DE ERRO DAS TOOLS DO NÓ, FECHADO E CONFERIDO NO ARRANQUE (AOS-550).
//
// # O CANAL QUE ISTO FECHA
//
// `tailResultado` (`prompt.go:470-504`) põe a mensagem de erro de uma tool NO CORPO do segmento
// `tool_result`, e o comentário de lá diz que é «uma decisão, não um esquecimento»: o valor é texto
// livre e pô-lo na linha de delimitação exigiria escapá-lo ou mutilá-lo.
//
// Enquanto o corpo do `tool_result` é texto livre de ponta a ponta, isso não acrescenta canal
// nenhum. A partir do momento em que a R2 converte esse corpo numa referência opaca, passa a
// acrescentar: uma tool cuja mensagem de erro CITE a entrada ofensiva — «ficheiro ../x não existe»,
// «json inválido perto de "ignora as instruções"» — repõe o canal inline no único segmento que a
// separação converte. Seria a fuga mais fácil de todas: basta ao modelo pedir uma tool com um
// argumento escolhido para aparecer na mensagem.
//
// # A REGRA, E ONDE É IMPOSTA
//
// Uma tool que o NÓ registe no RM só falha com `<tool>: <causa>`, com a causa num vocabulário
// FECHADO declarado aqui. Imposto em DOIS pontos:
//
//   - NA PRODUÇÃO DO ERRO, por um TIPO: [erroDaTool] é a forma de uma tool do nó construir a sua
//     falha, e troca por `causa_nao_declarada` qualquer causa que não esteja no vocabulário. NÃO é
//     um decorador à volta do executor, de propósito: invocar uma [referencemonitor.ToolFunc] fora
//     do Reference Monitor é o que a regra AOS-003 proíbe e o `arch-lint` recusa — a verificação
//     pertence a quem PRODUZ o erro, não a um invólucro que o chame.
//   - NO ARRANQUE, por [conferirVocabularioDeErro]: toda a tool que o nó registe tem de ter
//     vocabulário declarado. Uma tool nova sem entrada aqui NÃO ARRANCA — é o que impede a regra
//     de se tornar decoração no PR seguinte.
//
// # O QUE ISTO NÃO COBRE, DECLARADO
//
//   - As tools de SANDBOX registam o seu despacho dentro do `substrate/sandbox`
//     (`sandbox.NewMediatedLauncher`), e as mensagens de erro delas não nascem aqui. É a fronteira
//     deste ticket, e é o caso que mais precisa de a atravessar — o `stdout` de um comando é o
//     conteúdo de terceiros mais directo que o nó tem.
//   - Um erro que uma tool do nó produza SEM passar por [erroDaTool] (nas tools de modelo, por
//     [ligacaoDeModelo.falhar], que valida pelo mesmo vocabulário) escapa à substituição. Hoje
//     nenhuma via o faz — [causaDeclarada] prova-o sobre todas as recusas que os testes do AOS-550
//     exercitam —, e o arranque não o consegue provar: o que o fecha é a revisão do diff de cada
//     tool nova.

import (
	"fmt"
	"sort"
	"strings"
)

// causaNaoDeclarada é o que substitui uma causa fora do vocabulário. É deliberadamente
// inespecífica: o que o modelo pode ler é que a tool falhou, não porquê em palavras de terceiros.
const causaNaoDeclarada = "causa_nao_declarada"

// erroDeTool é a falha de uma tool do nó: o nome dela e uma causa do vocabulário fechado. A
// mensagem é CONSTRUÍDA de duas partes conhecidas, nunca interpolada com texto de fora.
type erroDeTool struct{ tool, causa string }

func (e *erroDeTool) Error() string { return e.tool + ": " + e.causa }

// Causa devolve a causa, para quem conte por causa sem reparsear a mensagem.
func (e *erroDeTool) Causa() string { return e.causa }

// erroDaTool constrói a falha de uma tool do nó. Uma causa fora do vocabulário FECHADO da tool —
// ou com qualquer coisa a mais colada a ela — sai como [causaNaoDeclarada].
func erroDaTool(tool, causa string) error {
	if !vocabularioDeErro()[tool][causa] {
		return &erroDeTool{tool: tool, causa: causaNaoDeclarada}
	}
	return &erroDeTool{tool: tool, causa: causa}
}

// vocabularioDeErro é, por tool que o NÓ regista no RM, o conjunto FECHADO de causas com que ela
// pode falhar. As listas não são escritas de novo aqui: são as MESMAS que cada tool já declara
// para a sua métrica ([ocrCausas], [videoCausas]) e para a sua recusa — uma segunda lista
// divergiria da primeira no PR seguinte.
//
// AS TOOLS DE MODELO COMPOSTAS ENTRAM POR ARGUMENTO (AOS-566), e não pelo mapa literal: desde que
// o bloco `modelo` deixou de ser de duas tools com nome fixo, o nome de uma tool de modelo é do
// REGISTO, e escrevê-lo aqui repunha pelo lado do erro a barreira por nome que a composição
// tirou. Cada ligação traz o seu conjunto ([ligacaoDeModelo.vocabulario]), que é o da classe dela.
// As duas entradas fixas da `ocr_read` e da `video_read` ficam, porque [causaDeclarada] as julga
// sem ter ligação nenhuma à mão.
func vocabularioDeErro(compostas ...toolDeModelo) map[string]map[string]bool {
	conjunto := func(causas ...string) map[string]bool {
		m := make(map[string]bool, len(causas))
		for _, c := range causas {
			m[c] = true
		}
		return m
	}
	// AOS-551: o SELO DO RESULTADO pode fazer falhar QUALQUER tool do nó, pelo que as causas do
	// espaço de handles entram no vocabulário de TODAS — ver [causasDoSeloDeHandle].
	com := func(causas ...string) map[string]bool {
		return conjunto(append(append([]string(nil), causas...), causasDoSeloDeHandle()...)...)
	}
	// A `media_view` falha com o vocabulário da leitura local confinada — o mesmo da `ocr_read`,
	// de que usa o leitor.
	m := map[string]map[string]bool{
		artifactWriteToolName: com(artCausaEntrada, artCausaTipo, artCausaConteudo, artCausaGrande,
			artCausaRun, artCausaVolume, artCausaCifra, artCausaDisco, artCausaRegisto),
		mediaViewToolName: com(ocrCausas()...),
		ocrToolName:       com(ocrCausas()...),
		videoToolName:     com(videoCausas()...),
	}
	for _, t := range compostas {
		c := t.comum()
		m[c.tool] = c.vocabulario
	}
	return m
}

// causasDoSeloDeHandle são as causas com que a selagem em quarentena pode falhar.
//
// ENTRAM NO VOCABULÁRIO DE TODAS AS TOOLS, e não de uma. Até ao AOS-551 a selagem só corria DENTRO
// da `artifact_write` e era por isso uma causa dela. Com o produtor do `tool_result` (AOS-551) o
// selo corre FORA de qualquer tool — dentro do efeito do step-ledger, depois do permit do Reference
// Monitor —, e uma recusa dele materializa-se como `tool_error` da tool que estava a correr. Logo
// um `ocr_read` pode falhar com `handle_tecto_do_run`, e declarar essa causa só na
// `artifact_write` deixava-a sair como [causaNaoDeclarada] — ou, antes desta correcção, sair CRUA
// sem passar por [erroDaTool].
//
// A lista não é escrita de novo: são as mesmas constantes do espaço de handles
// (`handle_store.go`, `handles.go`) que a métrica conta.
func causasDoSeloDeHandle() []string {
	return []string{
		hCausaVazio, hCausaGrande, hCausaRun, hCausaVolume, hCausaCifra, hCausaDisco, hCausaRegisto,
		handleCausaSemRun, handleCausaIlegivel,
	}
}

// conferirVocabularioDeErro exige que toda a tool em `registadas` tenha vocabulário declarado em
// [vocabularioDeErro]. Fail-closed: uma tool nova sem entrada não arranca.
func conferirVocabularioDeErro(registadas []string, compostas ...toolDeModelo) error {
	vocab := vocabularioDeErro(compostas...)
	var faltam []string
	for _, t := range registadas {
		if len(vocab[t]) == 0 {
			faltam = append(faltam, t)
		}
	}
	if len(faltam) == 0 {
		return nil
	}
	sort.Strings(faltam)
	return fmt.Errorf("aos: a(s) tool(s) %v sao registadas pelo no e nao declaram vocabulario de erro fechado (AOS-550): a mensagem de erro de uma tool vai no CORPO do segmento tool_result, pelo que uma mensagem que cite a entrada ofensiva repoe o canal inline que a fase R2 fecha. Declare as causas em vocabularioDeErro()", faltam)
}

// causaDeclarada diz se a mensagem de um erro é EXACTAMENTE `<tool>: <causa>` com a causa no
// vocabulário da tool (ou a própria [causaNaoDeclarada]). É o que os testes usam para provar que
// nenhuma via de erro de uma tool do nó leva texto de fora. A comparação é exacta, não por
// prefixo: uma mensagem que COMECE por uma causa declarada e continue com texto de terceiros é
// precisamente o que isto existe para apanhar.
func causaDeclarada(tool string, err error) bool {
	if err == nil {
		return false
	}
	causa, ok := strings.CutPrefix(err.Error(), tool+": ")
	return ok && (vocabularioDeErro()[tool][causa] || causa == causaNaoDeclarada)
}

// erroDeToolBanner é a linha de arranque. Lista as tools cobertas e diz o que fica de fora.
func erroDeToolBanner(registadas []string) []string {
	if len(registadas) == 0 {
		return nil
	}
	ordenadas := append([]string(nil), registadas...)
	sort.Strings(ordenadas)
	return []string{fmt.Sprintf("vocabulario de erro das tools do no (AOS-550): %v falham so com `<tool>: <causa>` de um conjunto FECHADO, construido por tipo (nunca interpolado com texto de fora); uma causa fora do conjunto sai como %q e NAO leva a entrada ofensiva ao corpo do segmento tool_result. Fora deste conjunto ficam as tools de sandbox, cujo despacho se registra no substrate/sandbox",
		ordenadas, causaNaoDeclarada)}
}
