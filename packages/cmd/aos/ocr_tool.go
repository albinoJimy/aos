package main

// ocr_tool.go — A TOOL `ocr_read` (AOS-530, peças 3a e 3b; ADR-042): uma imagem do run ou da pasta
// semeada, transcrita por um modelo de visão, PELO MODEL GATEWAY DO NÓ.
//
// É a primeira tool cujo efeito é uma chamada a um modelo. O desenho é o do ADR-042, e cada peça
// dele tem um sítio aqui:
//
//   - O REGISTO (§2.1). A tool entra pelo registo de tools do operador (`AOS_MODEL_TOOLS`) com um
//     bloco de ligação próprio, `"modelo": {"alias": "...", "classe": "imagem"}`, trusted e
//     exclusivo do bloco `sandbox`. O modelo que pede a tool escolhe só o nome dela e o `doc_id`;
//     o alias e os limites vêm do registo, a instrução deste ficheiro, nunca dos args. Desde o
//     AOS-566 o bloco não é de duas tools com nome fixo: qualquer entrada o pode ter, declarando a
//     CLASSE DE MEDIA que escolhe o efeito (classe_de_media.go) — o efeito (ler uma imagem e
//     transcrevê-la) continua a ser código, e só o USO dele é configuração.
//   - A MEDIAÇÃO (§2.1). A tool é uma [referencemonitor.CostingToolFunc] registada no RM do nó
//     com `RegisterCosting`, pela raiz de composição ([registarToolDeModelo], chamado pelo
//     Bootstrap). O RM decide como para qualquer tool — identidade, `cap:fs.read`, recurso `doc://{doc_id}`,
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
//   - A LEITURA (§2.4). O `doc_id` resolve-se por DUAS origens, nesta ordem (AOS-563,
//     [resolverDocDeImagem]): primeiro as ENTRADAS DE MEDIA do run no espaço de handles — o que quem
//     submeteu mandou no `POST /runs`, cifrado por titular, com o tipo que a porta conferiu nos
//     factos do handle —, e só depois a pasta semeada. Desta, uma montagem só de leitura por
//     `AOS_OCR_SEED_DIR` (não `AOS_SANDBOX_SEED_DIR`, que carrega a pasta inteira em memória),
//     confinada com [os.Root], só no nível de topo, sem seguir ligações simbólicas, só ficheiros
//     regulares (conferidos outra vez no descritor aberto), até [port.MaxImageBytes], PNG ou JPEG
//     pelos primeiros bytes — regras INALTERADAS pelo AOS-563. Tudo isto ANTES de chamar o modelo,
//     sem custo ([ligacaoOCR.lerImagem]).
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
	causas := []string{
		ocrResultadoTranscrito,
		ocrCausaEntrada, ocrCausaSemNHI,
		ocrCausaForaDaRaiz, ocrCausaNomeNaoCanonico, ocrCausaEmFalta, ocrCausaIlegivel, ocrCausaNaoRegular,
		ocrCausaVariasLigacoes, ocrCausaTrocado, ocrCausaGrande, ocrCausaFormato,
		ocrCausaTecto, ocrCausaOrcamento,
		ocrCausaTempo, ocrCausaCancelado, ocrCausaGateway, ocrCausaModeloServido,
		ocrCausaCortado, ocrCausaVazio, ocrCausaRespostaForma,
	}
	// AS CAUSAS DO ESPAÇO DE HANDLES CONTAM-SE NA MÉTRICA DA TOOL (AOS-563). Esta lista alimenta
	// TRÊS coisas: o vocabulário de erro fechado ([vocabularioDeErro], que já lhes acrescentava
	// estas), as séries do `/metrics` por resultado da tool (api.go) e o mapa de contadores que
	// [novaLigacaoDeModelo] constrói. Só as duas primeiras as tinham, pelo que uma chamada que
	// falhasse por elas saía ao modelo com a causa certa e NÃO somava em resultado nenhum: o
	// [ligacaoDeModelo.contar] ignora em silêncio um resultado fora do mapa.
	//
	// Era pré-existente (desde o produtor do `tool_result`, AOS-551), e o AOS-563 acrescentou-lhe um
	// caso NOVO e mais frequente — `handle_ilegivel`, com que a resolução do `doc_id` recusa quando o
	// espaço do run não se lê ou não decifra. Achado da segunda passagem adversarial. NÃO é uma
	// segunda cópia das strings: é a MESMA [causasDoSeloDeHandle] que o vocabulário usa.
	//
	// A `video_read` fica fora, com a sua própria lista: está fora do âmbito deste ticket e não
	// resolve no espaço de handles, pelo que a lacuna dela é a de sempre.
	return append(causas, causasDoSeloDeHandle()...)
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

// modeloMapping é o bloco `modelo` de uma entrada do registo de tools: o que o OPERADOR escolhe
// numa tool cujo efeito é uma chamada ao Model Gateway do nó.
//
// O ALIAS é o modelo que a tool chama. A CLASSE DE MEDIA escolhe, de entre os efeitos que o nó
// traz em código, qual é o desta tool (classe_de_media.go): é ela que substituiu o NOME como
// critério (AOS-566, emenda do ADR-043 §6 ao ADR-042 §2.1). Os LIMITES — o `max_tokens` da chamada
// e o prazo dela — são os da classe por omissão, e o registo pode declarar outros.
//
// A INSTRUÇÃO NÃO ESTÁ AQUI, e é decisão e não esquecimento: ela está presa por teste à do banco de
// ensaio que a mediu, e o registo de rota ASSINADO não tem membro de instrução. Movê-la para
// configuração sem esse trinco assinado seria uma regressão de segurança — passaria a mudar-se o
// que o modelo faz com o ficheiro sem acto do dono. Fica em Go, por classe.
//
// CAMPOS AUSENTES VALEM O DE HOJE: um manifesto escrito antes deste bloco ter classe e limites
// carrega sem alteração (ver [classePorOmissao]).
type modeloMapping struct {
	Alias string `json:"alias"`
	// Classe é a classe de media do efeito, do vocabulário FECHADO [classesDeMedia]. Ausente só
	// nas duas tools anteriores ao campo.
	Classe string `json:"classe,omitempty"`
	// MaxTokens é o `max_tokens` da chamada; 0 — a omissão — vale o da classe. Entra também na
	// reserva no tecto do run.
	MaxTokens int `json:"max_tokens,omitempty"`
	// PrazoSegundos é o prazo próprio da chamada ao gateway, em segundos; 0 vale o da classe.
	PrazoSegundos int `json:"prazo_s,omitempty"`
}

// validarLigacaoModelo confere, na leitura do registo, uma entrada com o bloco `modelo`. Sem o
// bloco não confere nada. A forma do recurso e a capability são FIXAS e iguais em todas as classes:
// o que o RM decide e o WORM sela tem de ser o que a tool lê.
//
// O QUE DEIXOU DE SER CONFERIDO É O NOME (AOS-566). Qualquer entrada pode ter o bloco, desde que
// declare uma CLASSE do vocabulário fechado — é a classe que escolhe o efeito, e o efeito continua
// a ser código do nó. Uma entrada sem classe que a ponte de compatibilidade não cubra é recusada
// por [limitesDoBlocoModelo]: o arranque continua fail-closed, pelo eixo certo.
func validarLigacaoModelo(s modelToolSpec) error {
	if s.Modelo == nil {
		return nil
	}
	if s.Sandbox != nil {
		return fmt.Errorf("%w: tool %q", ErrModelToolBindingConflict, strings.TrimSpace(s.Name))
	}
	nome := strings.TrimSpace(s.Name)
	// A CLASSE E OS LIMITES PRIMEIRO: sem classe não há efeito, e é a recusa que diz ao operador o
	// que falta declarar.
	if _, _, err := limitesDoBlocoModelo(s); err != nil {
		return err
	}
	switch {
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
//
// A COMPOSIÇÃO JÁ NÃO PASSA POR AQUI (AOS-566): o nó compõe uma ligação por entrada com bloco
// `modelo`, pela classe dela ([comporToolDeModelo]). Esta procura por nome fica para quem queira
// uma entrada CONCRETA — o anúncio por nome e as provas que nomeiam as duas tools de hoje.
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
	// handles é o plano de handles do nó (AOS-563): onde a tool procura o `doc_id` ANTES da pasta
	// semeada. nil ⇒ sem espaço de handles composto, e a tool lê só da pasta, como antes.
	//
	// VIVE AQUI E NÃO EM [ligacaoDeModelo] de propósito: a parte comum é partilhada com a
	// `video_read`, que está FORA do âmbito do AOS-563 (o tecto de um vídeo não é o de uma imagem).
	// Pô-lo lá alcançava-a sem que nada o tivesse pedido.
	handles *planoDeHandles
}

// errosDoOCR são os erros de arranque da `ocr_read`.
var errosDoOCR = errosDeArranque{
	foraDaAllowlist: ErrOCRAliasForaDaAllowlist, semRegisto: ErrOCRSemRegistoTool, semPreco: ErrOCRSemPreco, semPasta: ErrOCRSemPasta,
}

// comporLigacaoDeImagem confere as condições de ARRANQUE de uma tool da CLASSE IMAGEM e compõe a
// ligação, sem o gateway (que só existe depois). Corre na fronteira de ambiente
// ([parseModelFromEnv]), com o que lá já foi lido: a allowlist em vigor (externa, ou nil ⇒ a
// embebida), os perfis assinados, a fronteira de soberania e se a contabilidade de custo está
// composta. A ordem é a das conferências sem efeito primeiro ([conferirArranqueDeModelo]: a
// allowlist, um registo de uso `tool` DE IMAGEM, o preço); a pasta abre-se por último.
//
// É o efeito da classe, não o da `ocr_read`: o nome da tool e os limites vêm de `lim`, e a
// `ocr_read` é a configuração dela que o manifesto de produção declara.
func comporLigacaoDeImagem(lim limitesDaTool, spec *modelToolSpec, assinados *perfisAssinados, externa *allowlist.Policy, region, board string, comCusto bool) (toolDeModelo, error) {
	alias, perfilDigest, tabela, err := conferirArranqueDeModelo(spec, modelgateway.CapabilityImage, assinados, externa, region, board, comCusto, errosDoOCR)
	if err != nil {
		return nil, err
	}
	// O custo máximo por chamada que a tool reserva no tecto do run (peça 3b).
	reservaMax, err := reservaDaImagem(tabela, alias, region, lim.maxTokens)
	if err != nil {
		return nil, err
	}
	// A pasta semeada, aberta e confinada.
	raiz, pasta, err := abrirPastaSemeada(ErrOCRSemPasta)
	if err != nil {
		return nil, err
	}
	l := novaLigacaoDeImagem(lim, region, board, perfilDigest, pasta, raiz)
	l.reservaMax = reservaMax
	return l, nil
}

// novaLigacaoOCR monta a ligação da `ocr_read` com os limites POR OMISSÃO da classe imagem, sem o
// gateway. É a configuração de hoje da máquina da classe ([novaLigacaoDeImagem]).
func novaLigacaoOCR(alias, region, board, perfilDigest, pasta string, raiz *os.Root) *ligacaoOCR {
	lim := limitesDaTool{nome: ocrToolName, alias: alias, classe: port.MediaClassImage,
		maxTokens: ocrMaxTokens, prazo: ocrPrazo}
	return novaLigacaoDeImagem(lim, region, board, perfilDigest, pasta, raiz)
}

// novaLigacaoDeImagem monta a ligação de uma tool da classe imagem, sem o gateway.
func novaLigacaoDeImagem(lim limitesDaTool, region, board, perfilDigest, pasta string, raiz *os.Root) *ligacaoOCR {
	l := &ligacaoOCR{ligacaoDeModelo: novaLigacaoDeModelo(lim, "ocr_read (AOS-530)", region, board, perfilDigest, pasta, raiz,
		ocrDigestDaInstrucao(ocrInstrucaoSistema, ocrInstrucaoPedido), ocrCausas())}
	l.reservaMax = budget.Amount{Tokens: ocrTokensDeEntradaMax + int64(lim.maxTokens)}
	return l
}

// comum é a parte partilhada da ligação ([toolDeModelo]).
func (l *ligacaoOCR) comum() *ligacaoDeModelo { return &l.ligacaoDeModelo }

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

// comporReescritas junta a reescrita da sandbox e a das tools do nó ([juntarReescritasDeModelo]).
// Tocam em tools diferentes (os blocos são exclusivos): a das tools do nó só reescreve essas, e a
// da sandbox só as tools com binding. Qualquer uma nil ⇒ a outra; as duas nil ⇒ nil (sem reescrita,
// comportamento byte-idêntico).
//
// O DESPACHO É PELO BINDING, E NÃO POR UMA LISTA DE NOMES — e a diferença já custou uma regressão.
// Até ao AOS-563 isto era um `switch` sobre uma LISTA LITERAL de `ToolID` («é a `ocr_read`, a
// `video_read` ou a `artifact_write`? então a de modelo; senão a da sandbox»). A lista tinha de ser
// mantida à mão, e uma tool do nó que se esquecesse de se inscrever nela caía na reescrita ERRADA:
// a da sandbox devolve inalterada uma tool sem binding, pelo que o executor recebia os args CRUS do
// modelo e recusava-os. Aconteceu com a `artifact_write` (o `TestAOS548_ReescritaComposta` existe
// por isso) e voltou a acontecer com a `media_view` quando o AOS-563 lhe deu reescrita de efeito —
// e só num nó COM binding de sandbox, que é a forma de PRODUÇÃO (o catálogo tem a `doc_read` com
// bloco `sandbox`), porque sem bindings a da sandbox é nil e o `case` devolvia a de modelo. Um
// defeito que só aparece na forma de produção é o pior sítio para pôr uma lista à mão.
//
// `temBinding` É A PERGUNTA CERTA porque é a MESMA que a reescrita da sandbox se faz a si própria
// ([newSandboxEffectRewriter]: sem binding, devolve a call inalterada). Deixa de haver duas fontes
// para a mesma decisão, e uma tool do nó nova não tem onde se esquecer de se inscrever.
//
// CONTINUA EXCLUSIVO, e isso é de propósito: uma call vai a UMA reescrita, nunca às duas. Em cadeia
// seria equivalente — os blocos são exclusivos, imposto no ARRANQUE (`modelo` com `sandbox` na mesma
// spec aborta, [validarLigacaoModelo]/ErrModelToolBindingConflict; a `media_view` e a
// `artifact_write` recusam o bloco `sandbox`) — mas o exclusivo não depende dessa validação para
// estar correcto, e é a propriedade que os testes das tools de modelo asseram.
func comporReescritas(temBinding func(string) bool, sandboxRw, modeloRw func(referencemonitor.Call) (referencemonitor.Call, error)) func(referencemonitor.Call) (referencemonitor.Call, error) {
	switch {
	case modeloRw == nil:
		return sandboxRw
	case sandboxRw == nil:
		return modeloRw
	}
	return func(call referencemonitor.Call) (referencemonitor.Call, error) {
		if temBinding != nil && temBinding(call.ToolID) {
			return sandboxRw(call)
		}
		return modeloRw(call)
	}
}

// posturaDaOrigemDoDoc é a parte do banner que declara DE ONDE um `doc_id` se resolve (AOS-563).
//
// É CALCULADA DO QUE FOI COMPOSTO, e não afirmada — achado da segunda passagem adversarial, e a
// regra da casa é a do AOS-203/AOS-248: a postura ANUNCIADA é a postura LIGADA. As duas tools de
// imagem arrancam sem exigir o espaço de handles, e o `Bootstrap` só lhes liga o plano quando a VIA
// DE ENTRADA está composta (`AOS_ENTRADA_DE_MEDIA` com `AOS_HANDLE_DIR`). Um banner que afirmasse
// «duas origens» num nó que tem uma só mandava o operador procurar uma capacidade que ele não
// habilitou — e era a mesma classe de defeito que este ticket corrigiu no banner do AOS-561.
func posturaDaOrigemDoDoc(handles *planoDeHandles, pasta string) string {
	if handles == nil {
		return fmt.Sprintf("UMA so — o nivel de topo de %s=%q, montada so de leitura (regras do ADR-042 2.4). A via de entrada de media NAO esta composta neste no (AOS_ENTRADA_DE_MEDIA com AOS_HANDLE_DIR), logo nenhum doc_id resolve no espaco de handles", ocrSeedDirEnv, pasta)
	}
	return fmt.Sprintf("DUAS, nesta ordem (AOS-563): primeiro as entradas de media DESTE run no espaco de handles — o que quem submeteu mandou no POST /runs, pelo nome que o NO cunhou (%s-1..n), e de onde o tipo vem dos FACTOS do handle, o conferido na porta —, e so depois o nivel de topo de %s=%q, montada so de leitura (regras do ADR-042 2.4, INALTERADAS). NUNCA as duas para o mesmo doc_id: a colisao resolve pelo espaco do RUN e conta em aos_handle_total com resultado=%s", mediaInputHandleTool, ocrSeedDirEnv, pasta, hCausaDoisEspacos)
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

// lerImagem resolve a imagem `doc` e devolve a parte de imagem e o formato, ou a causa da recusa.
// Nada aqui chama o modelo.
//
// A ORDEM DE RESOLUÇÃO É A DO AOS-563, e vive em [resolverDocDeImagem]: primeiro as entradas de
// media do RUN no espaço de handles, depois a pasta semeada com os limites do ADR-042 §2.4. O
// `run` é o da tool call (vem em [entradaOCR], posto pela reescrita do efeito); vazio ⇒ só a pasta.
func (l *ligacaoOCR) lerImagem(ctx context.Context, run, doc string) (port.ImagePart, string, string) {
	dados, tipo, causa := resolverDocDeImagem(ctx, l.handles, l.raiz, run, doc, port.MaxImageBytes)
	if causa != "" {
		return port.ImagePart{}, "", causa
	}
	// DO ESPAÇO DE HANDLES O TIPO É O CONFERIDO NA PORTA (AOS-563), lido dos factos do handle: a
	// porta do AOS-561 já o decidiu pelos primeiros bytes antes de o run existir, e selou-o no
	// `handle.created`. Uma segunda inferência aqui era uma segunda derivação do mesmo tipo.
	if tipo != "" {
		return port.ImagePart{MediaType: tipo, Data: dados}, formatoDoTipoDeImagem(tipo), ""
	}
	// DA PASTA SEMEADA, O FORMATO PELOS PRIMEIROS BYTES, nunca pela extensão — inalterado. PDF e
	// tudo o resto ficam de fora.
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
// depois do permit. A sequência é a de qualquer tool de modelo ([executarDeModelo]); o que é da
// `ocr_read` é a classe da media que ela lê.
func (l *ligacaoOCR) executar(ctx context.Context, input []byte) ([]byte, int64, error) {
	return executarDeModelo(ctx, &l.ligacaoDeModelo, classeDeMedia{
		schema:    ocrInputSchema,
		sistema:   ocrInstrucaoSistema,
		pedido:    ocrInstrucaoPedido,
		maxTokens: l.maxTokens,
		sucesso:   ocrResultadoTranscrito,
		preparar:  l.prepararImagem,
	}, input)
}

// prepararImagem lê a imagem do `doc_id` e apura o que a chamada dela precisa. A reserva da
// `ocr_read` é SEMPRE o máximo por chamada ([reservaMax], composto no arranque: não depende da
// imagem), e uma resposta não medida cobra os tectos fixos da tool.
func (l *ligacaoOCR) prepararImagem(ctx context.Context, in entradaOCR) (pedidoDeMedia, string) {
	parte, formato, causa := l.lerImagem(ctx, in.RunID, in.DocID)
	if causa != "" {
		return pedidoDeMedia{}, causa
	}
	return pedidoDeMedia{
		parte:  port.MediaPart(parte),
		maximo: l.reservaMax,
		cobrar: tectoDaChamada{entrada: ocrTokensDeEntradaMax, saida: int64(l.maxTokens)},
		interpretar: func(resp port.ChatResponse, servido string) ([]byte, string) {
			return l.interpretar(resp, parte, formato, servido)
		},
	}, ""
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

// linhaDoArranque declara a tool ligada: o que ela faz, o que chama e o que esta versão NÃO faz.
func (l *ligacaoOCR) linhaDoArranque() string {
	return fmt.Sprintf("tool %s LIGADA (EPIC-07/AOS-530, ADR-042): le uma imagem PNG ou JPEG (ate %d bytes) e transcreve-a por UMA chamada ao Model Gateway deste no — o mesmo dos turnos —, com o token NHI do run e o run e o passo da tool call. Alias %q (board %q, regiao %q), perfil do registo assinado de uso tool %s; instrucao fixa %s; max_tokens %d; prazo %s; sem tools oferecidas ao modelo de visao; sem nova tentativa. Mediada pelo RM com %s e o recurso %s. ORIGEM DO doc_id: %s. CUSTO: %s",
		l.tool, port.MaxImageBytes, l.alias, l.board, l.region, l.perfilDigest, l.digestDaInstrucao, l.maxTokens, l.prazo, ocrCapability, ocrResourceValue, posturaDaOrigemDoDoc(l.handles, l.pasta), l.posturaDoCusto())
}

// posturaDoCusto declara, no arranque, o que acontece ao custo de cada chamada (ADR-042 §2.6).
func (l *ligacaoOCR) posturaDoCusto() string {
	return l.ligacaoDeModelo.posturaDoCusto(fmt.Sprintf("o maximo por chamada — %d tokens (%d de entrada + %d de saida) e %d micro-USD pela tabela de precos —",
		l.reservaMax.Tokens, ocrTokensDeEntradaMax, l.maxTokens, l.reservaMax.CostMicroUSD))
}

// ocrDoCliente devolve a ligação da classe IMAGEM do cliente de modelo dado, ou nil. É a série
// `aos_ocr_read_total` do `/metrics` e as provas que nomeiam a `ocr_read`.
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

// toolsDeModeloDoCliente devolve TODAS as tools de modelo compostas no cliente de modelo dado, pela
// ordem do registo. É por aqui que o Bootstrap as liga, regista e fecha, sem saber quantas são nem
// como se chamam.
func toolsDeModeloDoCliente(c agentruntime.ModelClient) []toolDeModelo {
	m, ok := c.(fonteDosPerfis)
	if !ok {
		return nil
	}
	if p := m.perfisAssinadosDoNo(); p != nil {
		return p.deModelo
	}
	return nil
}
