package main

// ocr_tool.go — A TOOL `ocr_read` (AOS-530, peças 3a e 3b; ADR-042): uma imagem da pasta semeada,
// transcrita por um modelo de visão, PELO MODEL GATEWAY DO NÓ.
//
// É a primeira tool cujo efeito é uma chamada a um modelo. O desenho é o do ADR-042, e cada peça
// dele tem um sítio aqui:
//
//   - O REGISTO (§2.1). A tool entra pelo registo de tools do operador (`AOS_MODEL_TOOLS`) com um
//     bloco de ligação próprio, `"modelo": {"alias": "..."}`, trusted e exclusivo do bloco
//     `sandbox`. O modelo que pede a tool escolhe só o nome dela e o `doc_id`; o alias, a
//     instrução e os limites vêm do registo e deste ficheiro, nunca dos args. Só a tool
//     `ocr_read` e a `video_read` (AOS-536, video_tool.go) podem ter o bloco: o efeito (ler uma
//     imagem e transcrevê-la) é código, não config.
//   - A MEDIAÇÃO (§2.1). A tool é uma [referencemonitor.CostingToolFunc] registada no RM do nó
//     com `RegisterCosting`, pela raiz de composição ([registarOCR], chamado pelo Bootstrap). O RM
//     decide como para qualquer tool — identidade, `cap:fs.read`, recurso `doc://{doc_id}`,
//     taint, risco, aprovação — e só depois do permit o despacho chama [ligacaoOCR.executar]. A
//     leitura da imagem e a chamada ao modelo são o efeito DESSA call; nenhuma é uma segunda.
//   - O GATEWAY (§2.1). O efeito é UMA chamada `Gateway.Chat` ao gateway que o nó já compõe para
//     os turnos — o MESMO objecto, guardado na composição ([newGatewayModelClientComPerfis]).
//     Nunca o proxy directamente, nunca a sandbox (sem rede por garantia).
//   - A IDENTIDADE (§2.2). O token NHI do run lê-se do contexto do despacho pela MESMA função que
//     o adaptador dos turnos usa ([modelCredentialFromContext]); o `RunID`/`StepID` são os da tool
//     call, que chegam ao input pela reescrita do efeito ([newOCREffectRewriter]), como no
//     `sandboxwiring.go`; e o pedido leva `OriginTool: "ocr_read"`. Sem credencial a tool falha
//     ANTES de ler o ficheiro: nunca se forja um principal (AOS-278).
//   - A LEITURA (§2.4). De uma montagem só de leitura da pasta semeada, por `AOS_OCR_SEED_DIR`
//     (não `AOS_SANDBOX_SEED_DIR`, que carrega a pasta inteira em memória), confinada com
//     [os.Root], só no nível de topo, sem seguir ligações simbólicas, só ficheiros regulares (conferidos
//     outra vez no descritor aberto), até [port.MaxImageBytes], PNG ou JPEG pelos primeiros bytes.
//     Tudo isto ANTES de chamar o modelo, sem custo ([ligacaoOCR.lerImagem]).
//   - O PEDIDO (§2.4). A instrução fixa e versionada que o AOS-529 mediu ([ocrInstrucaoSistema],
//     [ocrInstrucaoPedido]), na mesma forma — mensagem `system` com a instrução, mensagem `user`
//     com o pedido e a parte de imagem —; `Tools` vazio; `max_tokens` [ocrMaxTokens]; prazo
//     próprio [ocrPrazo].
//   - O RESULTADO (§2.5). JSON de forma fixa, `aos-ocr/v1` ([resultadoOCR]). É `untrusted` como o
//     de qualquer tool: quem o marca é o runtime, não a tool.
//   - AS FALHAS (§2.8). Causas de vocabulário fechado ([ocrCausas]) no erro da tool e na métrica
//     `aos_ocr_read_total`; nenhuma leva texto do modelo nem o corpo do provider. Sem nova
//     tentativa e sem failover.
//   - O QUE É PARTILHADO com a `video_read` (AOS-536) está em tool_de_modelo.go: a ligação, o
//     arranque, a chamada ao gateway e as falhas dela; a leitura confinada ([lerConfinado]) fica
//     aqui e o custo em ocr_custo.go, generalizados ao mínimo.
//   - O CUSTO (§2.6; peça 3b, em ocr_custo.go). Antes do pedido, a reserva do máximo por chamada no
//     tecto do run ([ligacaoDeModelo.reservar]); depois da resposta — aceite ou recusada, pela tool ou
//     pelo gateway —, o saldo pelo medido e o evento durável [EventTypeToolModelCost], que soma ao
//     consumo durável do run ([ligacaoDeModelo.fecharPago]). O custo volta também pelo canal do
//     `RegisterCosting`, ao span `aos.activity`, como o de qualquer tool.
//
// OS BYTES DA IMAGEM nunca entram num evento, span, métrica ou log: o resultado leva o digest, o
// tamanho e o formato; o pedido ao gateway leva os bytes numa [port.ImagePart], que só o wire do
// provider serializa.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	budget "github.com/aos-ref/control-plane/budget"
	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	referencemonitor "github.com/aos-ref/kernel/reference-monitor"
	modelgateway "github.com/aos-ref/platform/model-gateway"
	"github.com/aos-ref/platform/model-gateway/policy/allowlist"
	"github.com/aos-ref/platform/model-gateway/port"
)

// Constantes da tool. São do código, não do registo: mudá-las muda o que a tool faz.
const (
	// ocrToolName é o nome da tool — uma das duas que podem ter o bloco `modelo` no registo (a outra
	// é a `video_read`, AOS-536).
	ocrToolName = "ocr_read"
	// ocrResultSchema é a forma do resultado ([resultadoOCR]).
	ocrResultSchema = "aos-ocr/v1"
	// ocrInputSchema é a forma do input DEPOIS da reescrita do efeito ([entradaOCR]).
	ocrInputSchema = "aos-ocr-entrada/v1"
	// ocrMotorModelo é o motor desta ligação. A alternativa local (Tesseract no gVisor, ADR-042
	// §2.9) seria `local`, e NÃO está implementada.
	ocrMotorModelo = "modelo"
	// ocrMaxTokens é o `max_tokens` da chamada (ADR-042 §2.8, proposta 4096).
	ocrMaxTokens = 4096
	// ocrPrazo é o prazo próprio da chamada ao gateway, dentro do da tool call (ADR-042 §2.8,
	// proposta 60 s; a medição deu 2 a 9 s).
	ocrPrazo = 60 * time.Second
	// ocrSeedDirEnv é a variável da pasta semeada montada só de leitura.
	ocrSeedDirEnv = "AOS_OCR_SEED_DIR"
	// ocrCapability é a capability da tool: a mesma da `doc_read` (ADR-042 §2.8, uma leitura por
	// run, partilhada pelas duas).
	ocrCapability = "cap:fs.read"
	// ocrDocArg é o único argumento que o modelo escolhe.
	ocrDocArg = "doc_id"
	// ocrResourceValue é o recurso que o RM decide e o WORM sela.
	ocrResourceValue = "doc://{" + ocrDocArg + "}"
)

// A INSTRUÇÃO FIXA E VERSIONADA (ADR-042 §2.4) — a MESMA que o AOS-529 mediu
// (`packages/qa/banco-ensaio/ocr_visao.go`, `InstrucaoDaVisao` e `PedidoDaVisao`). Copiada e não
// importada: o banco é um módulo-folha que ninguém importa, e o nó não passa a depender dele. A
// igualdade é provada por teste (aos530_ocr_instrucao_test.go), que lê as constantes do banco do
// código-fonte e compara os digests: mudar uma das duas cópias parte o teste. Mudar a instrução
// muda a medição e pede uma nova corrida do banco e um registo assinado novo.
const (
	ocrInstrucaoSistema = "You are a text transcription engine. Transcribe all the text in the image exactly as it appears, line by line, keeping the original spelling, accents, letter case, numbers and punctuation. Do not translate, correct, summarise, explain or comment. Output only the transcribed text."
	ocrInstrucaoPedido  = "Transcribe the text in this image."
)

// ocrDigestDaInstrucao é o digest da instrução: `sha256:` sobre o JSON canónico das duas partes.
// É o que vai no resultado, para quem o lê saber com que instrução a imagem foi transcrita.
func ocrDigestDaInstrucao(sistema, pedido string) string {
	doc, _ := json.Marshal(struct {
		Pedido  string `json:"pedido"`
		Sistema string `json:"sistema"`
	}{pedido, sistema})
	soma := sha256.Sum256(doc)
	return "sha256:" + hex.EncodeToString(soma[:])
}

// As causas de falha da tool. Vocabulário FECHADO: é o que vai no erro da tool (e daí ao tail do
// run) e o rótulo da métrica. Nenhuma leva texto do modelo, corpo do provider, bytes da imagem ou
// o `doc_id`.
const (
	ocrCausaEntrada    = "entrada_invalida"
	ocrCausaSemNHI     = "sem_credencial"
	ocrCausaForaDaRaiz = "ficheiro_fora_da_raiz"
	ocrCausaEmFalta    = "ficheiro_em_falta"
	ocrCausaNaoRegular = "ficheiro_nao_regular"
	ocrCausaGrande     = "ficheiro_grande"
	// ocrCausaIlegivel — o ficheiro existe e o processo não o pode ler (permissão, erro de E/S).
	// Distinta de `ficheiro_fora_da_raiz`, que é só o que o [os.Root] recusa por escape, e de
	// `ficheiro_em_falta`: cada uma manda o operador procurar um defeito diferente.
	ocrCausaIlegivel = "ficheiro_ilegivel"
	// ocrCausaVariasLigacoes — um ficheiro regular com mais de uma ligação física (hardlink), em
	// unix: o mesmo inode pode estar também fora da raiz, e o [os.Root] não o vê. Causa própria, e
	// não `ficheiro_nao_regular`: o ficheiro É regular, e o operador tem de saber que é a ligação.
	ocrCausaVariasLigacoes = "ficheiro_com_varias_ligacoes"
	// ocrCausaNomeNaoCanonico — um nome que o sistema de ficheiros normaliza para outro (no
	// Windows, um `.` ou um espaço no fim): o ficheiro lido não seria o que o RM decidiu e o WORM
	// selou.
	ocrCausaNomeNaoCanonico = "nome_nao_canonico"
	// ocrCausaTrocado — o ficheiro aberto não é o que o Lstat do nome viu: o nome foi trocado entre
	// os dois (por exemplo por uma ligação simbólica para OUTRO ficheiro da raiz, que o [os.Root]
	// segue). Causa própria: o ficheiro aberto pode ser regular, e o que falhou foi a identidade.
	ocrCausaTrocado        = "ficheiro_trocado"
	ocrCausaFormato        = "formato_nao_aceite"
	ocrCausaTempo          = "tempo_esgotado"
	ocrCausaCancelado      = "cancelado"
	ocrCausaGateway        = "erro_do_gateway"
	ocrCausaModeloServido  = "modelo_servido_diferente"
	ocrCausaCortado        = "ocr_cortado"
	ocrCausaVazio          = "ocr_vazio"
	ocrCausaRespostaForma  = "resposta_invalida"
	ocrResultadoTranscrito = "transcrito"
)

// ocrCausas são todos os resultados, pela ordem da métrica: o de sucesso e as causas de falha.
func ocrCausas() []string {
	return []string{
		ocrResultadoTranscrito,
		ocrCausaEntrada, ocrCausaSemNHI,
		ocrCausaForaDaRaiz, ocrCausaNomeNaoCanonico, ocrCausaEmFalta, ocrCausaIlegivel, ocrCausaNaoRegular,
		ocrCausaVariasLigacoes, ocrCausaTrocado, ocrCausaGrande, ocrCausaFormato,
		ocrCausaTecto, ocrCausaOrcamento,
		ocrCausaTempo, ocrCausaCancelado, ocrCausaGateway, ocrCausaModeloServido,
		ocrCausaCortado, ocrCausaVazio, ocrCausaRespostaForma,
	}
}

// causaDaFalhaOCR devolve a causa de um erro da tool, ou "" se não é uma falha da tool. O erro é o
// das tools de modelo ([falhaDeTool]), `ocr_read: falha=<causa>`.
func causaDaFalhaOCR(err error) string { return causaDaFalhaDeTool(err) }

// Erros de ARRANQUE da tool. Fail-closed: com a `ocr_read` no registo de tools, o nó não arranca
// sem cada uma das condições (ADR-042 §2.3, §2.4; critério «Recusa sem o alias autorizado»).
var (
	// ErrModelToolBindingConflict — uma entrada do registo com os blocos `sandbox` e `modelo`.
	ErrModelToolBindingConflict = errors.New("aos: AOS_MODEL_TOOLS tem uma tool com os blocos `sandbox` E `modelo` — sao duas ligacoes do efeito, exclusivas: uma tool corre na sandbox OU chama um modelo pelo gateway do no (ADR-042 2.1)")
	// ErrOCRSemPasta — a pasta semeada da tool não está montada.
	ErrOCRSemPasta = errors.New("aos: a tool ocr_read esta no registo de tools e AOS_OCR_SEED_DIR nao aponta uma pasta legivel — a tool le as imagens da pasta semeada montada so de leitura (ADR-042 2.4); defina a variavel e monte a pasta, ou retire a tool do registo")
	// ErrOCRAliasForaDaAllowlist — o alias da tool não está na allowlist em vigor no nó.
	ErrOCRAliasForaDaAllowlist = errors.New("aos: o alias da tool ocr_read nao esta autorizado pela allowlist assinada em vigor no no, para o board e a regiao do no — o gateway negaria todas as chamadas (ADR-042 2.3); a allowlist re-assinada com o alias e um acto do dono")
	// ErrOCRSemRegistoTool — não há registo de entrada assinado de uso `tool` para o alias.
	ErrOCRSemRegistoTool = errors.New("aos: a tool ocr_read nao tem um registo de entrada assinado de uso tool para o seu alias na pasta de perfis de rota assinados (AOS_MODEL_ROUTE_PROFILES_DIR) — o perfil da rota (capacidade imagem, modelo esperado) chega so por esse registo (ADR-042 2.3)")
	// ErrOCRSemPreco — com a contabilidade de custo composta, o par (alias, região) não tem preço.
	ErrOCRSemPreco = errors.New("aos: a tool ocr_read esta no registo, a contabilidade de custo esta composta e o par (alias da tool, regiao do no) nao tem preco na tabela em vigor — a contabilidade recusaria todas as chamadas (ADR-042 2.3); acrescente o par a tabela (AOS_MODEL_PRICING_PATH)")
	// ErrOCRSemGateway — a tool está no registo e o nó não compôs o gateway de modelo a partir do
	// ambiente (sem AOS_MODEL_ENDPOINT, ou com um cliente de modelo injectado).
	ErrOCRSemGateway = errors.New("aos: a tool ocr_read esta no registo de tools e o no nao compos o Model Gateway a partir do ambiente (AOS_MODEL_ENDPOINT) — o efeito da tool e uma chamada a esse gateway, e nao ha outro caminho (ADR-042 2.1)")
)

// modeloMapping é o bloco `modelo` de uma entrada do registo de tools: o alias que a tool chama.
type modeloMapping struct {
	Alias string `json:"alias"`
}

// validarLigacaoModelo confere, na leitura do registo, uma entrada com o bloco `modelo`. Sem o
// bloco não confere nada. A forma do recurso e a capability são as da tool, fixas: o que o RM
// decide e o WORM sela tem de ser o que a tool lê. Só as tools `ocr_read` e `video_read` (AOS-536)
// podem ter o bloco, e as duas com a MESMA capability e o MESMO recurso (video_tool.go).
func validarLigacaoModelo(s modelToolSpec) error {
	if s.Modelo == nil {
		return nil
	}
	if s.Sandbox != nil {
		return fmt.Errorf("%w: tool %q", ErrModelToolBindingConflict, strings.TrimSpace(s.Name))
	}
	nome := strings.TrimSpace(s.Name)
	switch {
	case nome != ocrToolName && nome != videoToolName:
		return fmt.Errorf("%w: tool %q tem o bloco `modelo`, que so as tools %q e %q podem ter (o efeito e codigo do no, nao configuracao)", ErrBadModelTools, nome, ocrToolName, videoToolName)
	case strings.TrimSpace(s.Modelo.Alias) == "":
		return fmt.Errorf("%w: tool %q tem o bloco `modelo` sem `alias`", ErrBadModelTools, nome)
	case strings.TrimSpace(s.Capability) != ocrCapability:
		return fmt.Errorf("%w: tool %q tem de declarar capability %q (uma leitura por run, partilhada com a doc_read)", ErrBadModelTools, nome, ocrCapability)
	case strings.TrimSpace(s.ResourceValue) != ocrResourceValue:
		return fmt.Errorf("%w: tool %q tem de declarar resource_value %q (o recurso selado e o ficheiro lido)", ErrBadModelTools, nome, ocrResourceValue)
	case strings.ToLower(strings.TrimSpace(s.Egress)) != "external":
		// AOS-538: o efeito de uma tool de modelo é ENVIAR o ficheiro ao fornecedor da visão. Com
		// `egress: "none"` (ou implícito, que o contrato do digest lê como none) declarada a par de
		// `mutation: "none"` e `reversibility: "reversible"`, o gate de taint tratá-la-ia como
		// leitura local e deixá-la-ia passar com o contexto untrusted. O eixo é factual, não uma
		// escolha do operador: recusa o arranque em vez de confiar nele.
		return fmt.Errorf("%w: tool %q tem de declarar `egress: \"external\"` explicito — o ficheiro sai para o fornecedor do modelo (AOS-538)", ErrBadModelTools, nome)
	}
	return nil
}

// specDoOCR devolve a entrada do registo de tools `ocr_read` com o bloco `modelo`, ou nil sem ela.
func specDoOCR(specs []modelToolSpec) *modelToolSpec { return specDaToolDeModelo(specs, ocrToolName) }

// specDaToolDeModelo devolve a entrada do registo de tools com o nome dado e o bloco `modelo`, ou
// nil sem ela. Os nomes são únicos no registo ([readModelToolSpecs] recusa os repetidos).
func specDaToolDeModelo(specs []modelToolSpec, nome string) *modelToolSpec {
	for i := range specs {
		if specs[i].Modelo != nil && strings.TrimSpace(specs[i].Name) == nome {
			return &specs[i]
		}
	}
	return nil
}

// toolsDeModeloNoRegisto diz se o registo de tools do ambiente declara a `ocr_read` e a
// `video_read` com o bloco `modelo`.
func toolsDeModeloNoRegisto() (ocr, video bool, err error) {
	specs, err := readModelToolSpecs()
	if err != nil {
		return false, false, err
	}
	return specDoOCR(specs) != nil, specDaToolDeModelo(specs, videoToolName) != nil, nil
}

// gatewayDoOCR é o que a tool usa do gateway: uma chamada de chat. É o [*modelgateway.Gateway]
// do nó.
type gatewayDoOCR interface {
	Chat(context.Context, port.ChatRequest) (port.ChatResponse, error)
}

// ligacaoOCR é a tool composta: a parte comum das tools de modelo ([ligacaoDeModelo]: o alias, a
// fronteira de soberania, o perfil assinado, a pasta, o gateway do nó e o custo), com as constantes
// da `ocr_read`. A reserva por chamada é sempre o máximo ([reservaMaxDoOCR]).
type ligacaoOCR struct {
	ligacaoDeModelo
}

// errosDoOCR são os erros de arranque da `ocr_read`.
var errosDoOCR = errosDeArranque{
	foraDaAllowlist: ErrOCRAliasForaDaAllowlist, semRegisto: ErrOCRSemRegistoTool, semPreco: ErrOCRSemPreco, semPasta: ErrOCRSemPasta,
}

// comporLigacaoOCR confere as condições de ARRANQUE da tool e compõe a ligação, sem o gateway
// (que só existe depois). Corre na fronteira de ambiente ([parseModelFromEnv]), com o que lá já
// foi lido: a allowlist em vigor (externa, ou nil ⇒ a embebida), os perfis assinados, a fronteira
// de soberania e se a contabilidade de custo está composta. A ordem é a das conferências sem
// efeito primeiro ([conferirArranqueDeModelo]: a allowlist, um registo de uso `tool` DE IMAGEM, o
// preço); a pasta abre-se por último.
func comporLigacaoOCR(spec *modelToolSpec, assinados *perfisAssinados, externa *allowlist.Policy, region, board string, comCusto bool) (*ligacaoOCR, error) {
	alias, perfilDigest, tabela, err := conferirArranqueDeModelo(spec, modelgateway.CapabilityImage, assinados, externa, region, board, comCusto, errosDoOCR)
	if err != nil {
		return nil, err
	}
	// O custo máximo por chamada que a tool reserva no tecto do run (peça 3b).
	reservaMax, err := reservaMaxDoOCR(tabela, alias, region)
	if err != nil {
		return nil, err
	}
	// A pasta semeada, aberta e confinada.
	raiz, pasta, err := abrirPastaSemeada(ErrOCRSemPasta)
	if err != nil {
		return nil, err
	}
	l := novaLigacaoOCR(alias, region, board, perfilDigest, pasta, raiz)
	l.reservaMax = reservaMax
	return l, nil
}

// novaLigacaoOCR monta a ligação com as constantes da tool, sem o gateway.
func novaLigacaoOCR(alias, region, board, perfilDigest, pasta string, raiz *os.Root) *ligacaoOCR {
	l := &ligacaoOCR{novaLigacaoDeModelo(ocrToolName, "ocr_read (AOS-530)", alias, region, board, perfilDigest, pasta, raiz,
		ocrPrazo, ocrDigestDaInstrucao(ocrInstrucaoSistema, ocrInstrucaoPedido), ocrCausas())}
	l.reservaMax = budget.Amount{Tokens: ocrTokensDeEntradaMax + ocrMaxTokens}
	return l
}

// fechar liberta a pasta. Idempotente e nil-safe.
func (l *ligacaoOCR) fechar() {
	if l != nil {
		l.ligacaoDeModelo.fechar()
	}
}

// lido devolve o total de um resultado. Nil-safe.
func (l *ligacaoOCR) lido(resultado string) int64 {
	if l == nil {
		return 0
	}
	return l.ligacaoDeModelo.lido(resultado)
}

// entradaOCR é o input da tool DEPOIS da reescrita do efeito: o `doc_id` que o modelo escolheu,
// com o run e o passo da tool call.
type entradaOCR struct {
	Schema string `json:"schema"`
	RunID  string `json:"run_id"`
	StepID string `json:"step_id"`
	DocID  string `json:"doc_id"`
}

// resultadoOCR é o resultado `aos-ocr/v1`. Forma fixa: mudar um campo é uma versão nova.
type resultadoOCR struct {
	Schema string `json:"schema"`
	Motor  string `json:"motor"`
	Texto  string `json:"texto"`
	// SHA256, Bytes e Formato descrevem a imagem lida — nunca os bytes dela.
	SHA256  string `json:"sha256"`
	Bytes   int    `json:"bytes"`
	Formato string `json:"formato"`
	// Alias é o alias pedido; ModeloServido, o que o proxy declarou ter servido.
	Alias         string `json:"alias"`
	ModeloServido string `json:"modelo_servido"`
	// PerfilDigest é o digest do perfil da rota (do registo assinado de uso `tool`).
	PerfilDigest string `json:"perfil_digest"`
	// InstrucaoDigest é o digest da instrução fixa ([ocrDigestDaInstrucao]).
	InstrucaoDigest string `json:"instrucao_digest"`
}

// newOCREffectRewriter devolve a reescrita do efeito da `ocr_read` ([newEfeitoDeModeloRewriter]).
func newOCREffectRewriter() func(referencemonitor.Call) (referencemonitor.Call, error) {
	return newEfeitoDeModeloRewriter(ocrToolName, ocrInputSchema)
}

// newEfeitoDeModeloRewriter devolve a reescrita do efeito de uma tool de modelo: os args do modelo
// — SÓ o `doc_id` — passam a [entradaOCR] com o schema da tool, com o RunID e o StepID REAIS da
// Call (que só existem aqui). Como em [newSandboxEffectRewriter], não toca na decisão (principal,
// capability, recurso, taint); um erro vira Deny fail-closed no loop. Uma Call de outra tool passa
// inalterada.
func newEfeitoDeModeloRewriter(tool, schema string) func(referencemonitor.Call) (referencemonitor.Call, error) {
	return func(call referencemonitor.Call) (referencemonitor.Call, error) {
		if call.ToolID != tool {
			return call, nil
		}
		var args map[string]json.RawMessage
		if err := json.Unmarshal(call.Input, &args); err != nil {
			return referencemonitor.Call{}, errors.New(tool + ": argumentos ilegiveis")
		}
		if len(args) != 1 {
			return referencemonitor.Call{}, fmt.Errorf("%s: o unico argumento e %q", tool, ocrDocArg)
		}
		var doc string
		if err := json.Unmarshal(args[ocrDocArg], &doc); err != nil || doc == "" {
			return referencemonitor.Call{}, fmt.Errorf("%s: %q tem de ser texto nao vazio", tool, ocrDocArg)
		}
		if call.RunID == "" || call.StepID == "" {
			return referencemonitor.Call{}, errors.New(tool + ": tool call sem run ou passo")
		}
		raw, err := json.Marshal(entradaOCR{Schema: schema, RunID: call.RunID, StepID: call.StepID, DocID: doc})
		if err != nil {
			return referencemonitor.Call{}, err
		}
		call.Input = raw
		return call, nil
	}
}

// juntarReescritasDeModelo junta as reescritas das tools de modelo compostas (a `ocr_read` e a
// `video_read`): cada uma só reescreve a sua tool e deixa as outras inalteradas, pelo que se aplicam
// em sequência. As nil saem; nenhuma ⇒ nil.
func juntarReescritasDeModelo(rws ...func(referencemonitor.Call) (referencemonitor.Call, error)) func(referencemonitor.Call) (referencemonitor.Call, error) {
	var as []func(referencemonitor.Call) (referencemonitor.Call, error)
	for _, rw := range rws {
		if rw != nil {
			as = append(as, rw)
		}
	}
	switch len(as) {
	case 0:
		return nil
	case 1:
		return as[0]
	}
	return func(call referencemonitor.Call) (referencemonitor.Call, error) {
		for _, rw := range as {
			var err error
			if call, err = rw(call); err != nil {
				return referencemonitor.Call{}, err
			}
		}
		return call, nil
	}
}

// comporReescritas junta a reescrita da sandbox e a das tools de modelo (a `ocr_read` e a
// `video_read`, [juntarReescritasDeModelo]). Tocam em tools diferentes (os blocos são exclusivos):
// a das tools de modelo só reescreve essas, e a da sandbox só as tools com binding. Qualquer uma nil
// ⇒ a outra; as duas nil ⇒ nil (sem reescrita, comportamento byte-idêntico).
func comporReescritas(sandboxRw, modeloRw func(referencemonitor.Call) (referencemonitor.Call, error)) func(referencemonitor.Call) (referencemonitor.Call, error) {
	switch {
	case modeloRw == nil:
		return sandboxRw
	case sandboxRw == nil:
		return modeloRw
	}
	return func(call referencemonitor.Call) (referencemonitor.Call, error) {
		if call.ToolID == ocrToolName || call.ToolID == videoToolName {
			return modeloRw(call)
		}
		return sandboxRw(call)
	}
}

// raizDoOCR é o que a leitura usa da pasta: o Lstat do nome e a abertura só de leitura. É o
// [os.Root] da pasta ([raizOS]); os testes trocam-no para simular o que o disco não deixa simular
// em todos os sistemas (sem permissão, um ficheiro trocado entre o Lstat e a abertura).
type raizDoOCR interface {
	Lstat(nome string) (fs.FileInfo, error)
	abrir(nome string) (ficheiroDoOCR, error)
	// mesmoFicheiro diz se dois FileInfo são o mesmo ficheiro ([os.SameFile] na raiz real).
	mesmoFicheiro(a, b fs.FileInfo) bool
}

// ficheiroDoOCR é o ficheiro aberto: lê-se, e o Stat é sobre o DESCRITOR, não sobre o caminho.
type ficheiroDoOCR interface {
	io.Reader
	Stat() (fs.FileInfo, error)
	Close() error
}

// raizOS é o [os.Root] da pasta semeada.
type raizOS struct{ *os.Root }

// abrir abre só de leitura, com [ocrFlagsDeAbertura] (O_NONBLOCK em unix: um FIFO posto no lugar
// do ficheiro entre o Lstat e a abertura não pendura a tool; a verificação no descritor recusa-o).
func (r raizOS) abrir(nome string) (ficheiroDoOCR, error) {
	return r.OpenFile(nome, os.O_RDONLY|ocrFlagsDeAbertura, 0)
}

// mesmoFicheiro é [os.SameFile]: o mesmo dispositivo e o mesmo inode (no Windows, o mesmo volume e
// o mesmo índice de ficheiro).
func (raizOS) mesmoFicheiro(a, b fs.FileInfo) bool { return os.SameFile(a, b) }

// ocrMaxNome é o tecto, em bytes, do nome de uma imagem: o de um componente de caminho na maioria
// dos sistemas. Um nome maior nunca existe na pasta, e é um erro de entrada, não um escape.
const ocrMaxNome = 255

// causaDoErroDeFicheiro classifica um erro do Lstat ou da abertura: em falta, sem permissão, ou —
// o resto, que é o que o [os.Root] recusa por escape — fora da raiz.
func causaDoErroDeFicheiro(err error) string {
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return ocrCausaEmFalta
	case errors.Is(err, fs.ErrPermission):
		return ocrCausaIlegivel
	case errors.Is(err, os.ErrClosed):
		// A pasta foi fechada: o nó está a encerrar.
		return ocrCausaCancelado
	case nomeDemasiadoLongo(err):
		return ocrCausaEntrada
	default:
		return ocrCausaForaDaRaiz
	}
}

// conferirFicheiro é a decisão sobre um FileInfo: regular, até ao tecto de bytes da media, e uma só
// ligação física.
func conferirFicheiro(info fs.FileInfo, maximo int64) string {
	switch {
	case !info.Mode().IsRegular():
		// Inclui as ligações simbólicas: no nível de topo não se seguem, nem para dentro da raiz.
		return ocrCausaNaoRegular
	case info.Size() > maximo:
		return ocrCausaGrande
	case ligacoesMultiplas(info):
		return ocrCausaVariasLigacoes
	}
	return ""
}

// lerImagem lê a imagem `doc` da pasta, com os limites do ADR-042 §2.4, e devolve a parte de
// imagem e o formato, ou a causa da recusa. Nada aqui chama o modelo.
func (l *ligacaoOCR) lerImagem(doc string) (port.ImagePart, string, string) {
	dados, causa := lerConfinado(l.raiz, doc, port.MaxImageBytes)
	if causa != "" {
		return port.ImagePart{}, "", causa
	}
	// O FORMATO PELOS PRIMEIROS BYTES, nunca pela extensão. PDF e tudo o resto ficam de fora.
	for _, c := range []struct{ tipo, formato string }{{port.ImageMediaPNG, "png"}, {port.ImageMediaJPEG, "jpeg"}} {
		parte := port.ImagePart{MediaType: c.tipo, Data: dados}
		if parte.Validate() == nil {
			return parte, c.formato, ""
		}
	}
	return port.ImagePart{}, "", ocrCausaFormato
}

// lerConfinado lê os bytes do ficheiro `doc` da pasta semeada, até `maximo` bytes, com as regras
// do ADR-042 §2.4 — as mesmas para as duas tools de modelo —, ou devolve a causa da recusa. O
// formato é da tool que chama. Nada aqui chama o modelo.
//
// A DECISÃO FINAL É SOBRE O DESCRITOR. O Lstat do nome é um filtro barato (recusa cedo uma ligação
// simbólica, uma pasta, um ficheiro grande); mas entre ele e a abertura o nome pode ser trocado. Por
// isso, depois de abrir, o Stat do DESCRITOR volta a conferir tudo — regular, tamanho, ligações — e
// a leitura é limitada ao tecto. O que se lê é o que se conferiu.
func lerConfinado(raiz raizDoOCR, doc string, maximo int) ([]byte, string) {
	// SÓ O NÍVEL DE TOPO. Um separador, `..`, um caminho absoluto, uma unidade ou um `:` nunca
	// nomeiam um ficheiro do topo da pasta: recusados antes de tocar no disco. O [os.Root] recusa
	// de novo o que sair da raiz.
	if doc == "." || doc == ".." || strings.ContainsAny(doc, `/\:`+"\x00") || filepath.IsAbs(doc) || filepath.VolumeName(doc) != "" {
		return nil, ocrCausaForaDaRaiz
	}
	// UM NOME QUE O SISTEMA NORMALIZA lê outro ficheiro que o selado: no Windows, `fatura.png.` e
	// `fatura.png ` abrem `fatura.png`. Recusados em todos os sistemas (nenhum nome legítimo da pasta
	// acaba assim). A diferença de maiúsculas num sistema que não as distingue fica declarada no
	// README do nó.
	if len(doc) > ocrMaxNome {
		return nil, ocrCausaEntrada
	}
	if strings.HasSuffix(doc, ".") || strings.HasSuffix(doc, " ") {
		return nil, ocrCausaNomeNaoCanonico
	}
	info, err := raiz.Lstat(doc)
	if err != nil {
		return nil, causaDoErroDeFicheiro(err)
	}
	if causa := conferirFicheiro(info, int64(maximo)); causa != "" {
		return nil, causa
	}
	f, err := raiz.abrir(doc)
	if err != nil {
		return nil, causaDoErroDeFicheiro(err)
	}
	defer f.Close()
	noDescritor, err := f.Stat()
	if err != nil {
		return nil, ocrCausaIlegivel
	}
	if causa := conferirFicheiro(noDescritor, int64(maximo)); causa != "" {
		return nil, causa
	}
	// O MESMO FICHEIRO que o Lstat viu. O [os.Root] segue ligações simbólicas que fiquem dentro da
	// raiz, e o O_NOFOLLOW não o impede: trocar o nome por uma ligação para OUTRO ficheiro da raiz
	// entre o Lstat e a abertura faria ler outro ficheiro que não o selado.
	if !raiz.mesmoFicheiro(info, noDescritor) {
		return nil, ocrCausaTrocado
	}
	// O tamanho volta a ser limitado na LEITURA: o ficheiro pode crescer depois do Stat.
	dados, err := io.ReadAll(io.LimitReader(f, int64(maximo)+1))
	if err != nil {
		return nil, ocrCausaIlegivel
	}
	if len(dados) > maximo {
		return nil, ocrCausaGrande
	}
	return dados, ""
}

// executar é a [referencemonitor.CostingToolFunc] da `ocr_read`. Só o despacho do RM a chama,
// depois do permit.
func (l *ligacaoOCR) executar(ctx context.Context, input []byte) ([]byte, int64, error) {
	var in entradaOCR
	if err := json.Unmarshal(input, &in); err != nil || in.Schema != ocrInputSchema || in.DocID == "" || in.RunID == "" || in.StepID == "" {
		return l.falhar(ocrCausaEntrada, 0)
	}
	// A IDENTIDADE ANTES DE TUDO: sem o token do run não se lê o ficheiro (ADR-042 §2.2).
	credencial := modelCredentialFromContext(ctx)
	if credencial == "" {
		return l.falhar(ocrCausaSemNHI, 0)
	}
	parte, formato, causa := l.lerImagem(in.DocID)
	if causa != "" {
		return l.falhar(causa, 0)
	}
	// A RESERVA (ADR-042 §2.6): com a imagem lida e antes do pedido, o máximo por chamada no tecto
	// do run. Não cabe ⇒ negada sem chamar o modelo.
	reserva, causa := l.reservar(ctx, in.RunID, l.reservaMax)
	if causa != "" {
		return l.falhar(causa, 0)
	}
	return l.chamar(ctx, in, credencial, reserva, tectoDaChamada{entrada: ocrTokensDeEntradaMax, saida: ocrMaxTokens},
		[]port.Message{
			{Role: port.RoleSystem, Content: ocrInstrucaoSistema},
			{Role: port.RoleUser, Content: ocrInstrucaoPedido, Images: []port.ImagePart{parte}},
		}, ocrMaxTokens, ocrResultadoTranscrito,
		func(resp port.ChatResponse, servido string) ([]byte, string) {
			return l.interpretar(resp, parte, formato, servido)
		})
}

// interpretar decide o que uma resposta do modelo é: o resultado `aos-ocr/v1` e `transcrito`, ou a
// causa da recusa ([textoDaResposta]: vazia, cortada, com uma tool call).
func (l *ligacaoOCR) interpretar(resp port.ChatResponse, parte port.ImagePart, formato, servido string) ([]byte, string) {
	texto, causa := textoDaResposta(resp, ocrCausaCortado, ocrCausaVazio)
	if causa != "" {
		return nil, causa
	}
	out, err := json.Marshal(resultadoOCR{
		Schema: ocrResultSchema, Motor: ocrMotorModelo, Texto: texto,
		SHA256: parte.Digest(), Bytes: len(parte.Data), Formato: formato,
		Alias: l.alias, ModeloServido: servido, PerfilDigest: l.perfilDigest,
		InstrucaoDigest: l.digestDaInstrucao,
	})
	if err != nil {
		return nil, ocrCausaRespostaForma
	}
	return out, ocrResultadoTranscrito
}

// registarOCR regista a `ocr_read` no RM do nó com RegisterCosting. Corre no Bootstrap, DEPOIS
// de o runtime seguro existir. Fail-closed: sem o gateway ligado, ou com o registo recusado, o
// arranque aborta.
func registarOCR(sec interface {
	RegisterCosting(string, referencemonitor.CostingToolFunc) error
}, l *ligacaoOCR, log func(string, ...any)) error {
	if l == nil {
		return nil
	}
	if l.gw == nil {
		return ErrOCRSemGateway
	}
	if err := sec.RegisterCosting(ocrToolName, l.executar); err != nil {
		return fmt.Errorf("aos: registar a tool %s: %w", ocrToolName, err)
	}
	log("%s", l.linhaDoArranque())
	return nil
}

// linhaDoArranque declara a tool ligada: o que ela faz, o que chama e o que esta versão NÃO faz.
func (l *ligacaoOCR) linhaDoArranque() string {
	return fmt.Sprintf("tool %s LIGADA (EPIC-07/AOS-530, ADR-042): le uma imagem PNG ou JPEG (ate %d bytes) do nivel de topo de %s=%q, montada so de leitura, e transcreve-a por UMA chamada ao Model Gateway deste no — o mesmo dos turnos —, com o token NHI do run e o run e o passo da tool call. Alias %q (board %q, regiao %q), perfil do registo assinado de uso tool %s; instrucao fixa %s; max_tokens %d; prazo %s; sem tools oferecidas ao modelo de visao; sem nova tentativa. Mediada pelo RM com %s e o recurso %s. CUSTO: %s",
		ocrToolName, port.MaxImageBytes, ocrSeedDirEnv, l.pasta, l.alias, l.board, l.region, l.perfilDigest, l.digestDaInstrucao, ocrMaxTokens, l.prazo, ocrCapability, ocrResourceValue, l.posturaDoCusto())
}

// posturaDoCusto declara, no arranque, o que acontece ao custo de cada chamada (ADR-042 §2.6).
func (l *ligacaoOCR) posturaDoCusto() string {
	return l.ligacaoDeModelo.posturaDoCusto(fmt.Sprintf("o maximo por chamada — %d tokens (%d de entrada + %d de saida) e %d micro-USD pela tabela de precos —",
		l.reservaMax.Tokens, ocrTokensDeEntradaMax, ocrMaxTokens, l.reservaMax.CostMicroUSD))
}

// ocrDoCliente devolve a ligação da `ocr_read` do cliente de modelo dado, ou nil.
func ocrDoCliente(c agentruntime.ModelClient) *ligacaoOCR {
	m, ok := c.(fonteDosPerfis)
	if !ok {
		return nil
	}
	if p := m.perfisAssinadosDoNo(); p != nil {
		return p.ocr
	}
	return nil
}
