package main

// MEDIA NO TURNO DO MODELO DO RUN (AOS-546, ADR-043 §2.2) — `AOS_MEDIA_NO_TURNO`.
//
// O modelo do run passa a VER uma imagem: a tool LOCAL `media_view` lê uma imagem PNG ou JPEG do
// nível de topo da pasta semeada (`AOS_OCR_SEED_DIR`, a mesma das tools de modelo) e devolve uma
// REFERÊNCIA — o documento, o `sha256`, o tipo e o tamanho ([agentruntime.MediaRef]), nunca os
// bytes. No layout 1.7.0 o turno seguinte do run leva um segmento `media` com a referência, e o
// adaptador do gateway resolve os bytes pela mesma pasta, confere o digest e envia a imagem ao
// modelo do run como parte da mensagem `user` desse segmento.
//
//   - `off` (por omissão): nada muda. Os runs novos ficam nos layouts de sempre e a tool não existe.
//   - `on`: os runs NOVOS ficam no layout 1.7.0; a `media_view` é registada no RM se o registo de
//     tools a declarar; o adaptador resolve a media do tail.
//
// NÃO HÁ EGRESS NOVO. A tool não sai do nó. A imagem vai ao provider do MODELO DO RUN — o mesmo que
// já recebe o tail inteiro em cada turno —, e só se a rota resolvida declarar a capacidade
// `imagem` no seu perfil: sem ela, o gateway recusa o pedido antes de sair
// (`ErrRouteWithoutImage`), e o run falha com essa causa.
//
// O TAINT NÃO MUDA. O resultado da tool e o segmento `media` são untrusted. Exige o «pedir antes de
// ler» (`AOS_PLAN_INPUT_DEPOIS_DO_PRIMEIRO_TURNO=on`): o layout 1.7.0 é a 1.6.0 com a media, e ligá-lo
// sem o interruptor dela adiaria os `plan_input` sem o operador o ter pedido.
//
// O REPLAY não precisa dos bytes: o segmento é função do resultado capturado, e o `prompt_hash`
// compromete-se com a imagem pelo digest. Uma RETOMA ao vivo precisa deles: se o ficheiro já não
// estiver na pasta, ou mudou, o turno falha com `ErrMediaUnavailable` em vez de sair sem a imagem.
//
// RECUO: voltar a `off`. Os runs começados em 1.7.0 continuam nela; sem o resolvedor, um turno com
// media falha (fail-closed).

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync/atomic"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	modelgateway "github.com/aos-ref/platform/model-gateway"
	"github.com/aos-ref/platform/model-gateway/port"
)

// Os dois valores de AOS_MEDIA_NO_TURNO.
const (
	mediaNoTurnoOff = "off"
	mediaNoTurnoOn  = "on"
)

// mediaViewToolName é o nome da tool. É código do nó: o efeito (ler e referenciar) não é
// configuração, como o das tools de modelo.
const mediaViewToolName = agentruntime.MediaViewTool

// Formas que o registo de tools tem de declarar para a `media_view`: uma leitura local do mesmo
// recurso das tools de modelo.
const (
	mediaViewCapability    = ocrCapability
	mediaViewResourceValue = ocrResourceValue
	mediaViewEgress        = "none"
)

var (
	// ErrBadMediaNoTurno — AOS_MEDIA_NO_TURNO fora do vocabulário. O nó recusa arrancar.
	ErrBadMediaNoTurno = errors.New("aos: AOS_MEDIA_NO_TURNO invalida — valores aceites: off (por omissao; o modelo do run nao ve media) e on (os runs novos ficam no layout 1.7.0 e a tool media_view envia a imagem ao modelo do run no turno seguinte)")
	// ErrMediaNoTurnoSemPedirAntesDeLer — `on` sem AOS_PLAN_INPUT_DEPOIS_DO_PRIMEIRO_TURNO=on.
	ErrMediaNoTurnoSemPedirAntesDeLer = errors.New("aos: AOS_MEDIA_NO_TURNO=on exige AOS_PLAN_INPUT_DEPOIS_DO_PRIMEIRO_TURNO=on — o layout 1.7.0 e o 1.6.0 com a media")
	// ErrMediaNoTurnoSemTool — `on` sem a `media_view` no registo de tools, ou declarada noutra forma.
	ErrMediaNoTurnoSemTool = errors.New("aos: AOS_MEDIA_NO_TURNO=on exige a tool media_view no registo AOS_MODEL_TOOLS, com capability cap:fs.read, recurso doc://{doc_id}, egress none, mutation none e sem bloco sandbox nem modelo")
	// ErrMediaNoTurnoSemPasta — `on` sem a pasta semeada legível.
	ErrMediaNoTurnoSemPasta = errors.New("aos: AOS_MEDIA_NO_TURNO=on exige AOS_OCR_SEED_DIR, a pasta semeada de onde a media_view le as imagens")
	// ErrMediaNoTurnoSemGateway — `on` sem o Model Gateway do nó: a imagem só chega ao modelo por ele.
	ErrMediaNoTurnoSemGateway = errors.New("aos: AOS_MEDIA_NO_TURNO=on exige o Model Gateway do no (AOS_MODEL_ENDPOINT): a imagem so chega ao modelo do run pelo adaptador dele")
)

// parseMediaNoTurnoFromEnv lê AOS_MEDIA_NO_TURNO. Vazia ⇒ false (`off`).
func parseMediaNoTurnoFromEnv() (bool, error) {
	switch raw := strings.TrimSpace(os.Getenv("AOS_MEDIA_NO_TURNO")); raw {
	case "", mediaNoTurnoOff:
		return false, nil
	case mediaNoTurnoOn:
		return true, nil
	default:
		return false, fmt.Errorf("%w (veio %q)", ErrBadMediaNoTurno, raw)
	}
}

// opcaoDoAdaptador é uma opção do adaptador RT→GW, com o nome que o main.go usa sem importar o
// gateway.
type opcaoDoAdaptador = modelgateway.RuntimeAdapterOption

// ligacaoMedia é a `media_view` ligada: a pasta semeada aberta e confinada, e os contadores.
type ligacaoMedia struct {
	root  *os.Root
	raiz  raizDoOCR
	pasta string
	// lidas e recusadas contam as chamadas da tool; resolvidas e falhadas, as resoluções do
	// adaptador.
	lidas, recusadas, resolvidas, falhadas atomic.Int64
}

// comporMediaNoTurno compõe a ligação quando o interruptor está `on`: confere o «pedir antes de
// ler», a declaração da tool no registo e a pasta semeada. `off` ⇒ nil, sem ler nada.
func comporMediaNoTurno(ligado, pedirAntesDeLer bool) (*ligacaoMedia, error) {
	if !ligado {
		return nil, nil
	}
	if !pedirAntesDeLer {
		return nil, ErrMediaNoTurnoSemPedirAntesDeLer
	}
	specs, err := readModelToolSpecs()
	if err != nil {
		return nil, err
	}
	if err := conferirSpecDaMedia(specs); err != nil {
		return nil, err
	}
	raiz, pasta, err := abrirPastaSemeada(ErrMediaNoTurnoSemPasta)
	if err != nil {
		return nil, err
	}
	return &ligacaoMedia{root: raiz, raiz: raizOS{raiz}, pasta: pasta}, nil
}

// conferirSpecDaMedia exige a `media_view` declarada como leitura local do recurso das tools de
// modelo, sem executor de sandbox nem bloco de modelo.
func conferirSpecDaMedia(specs []modelToolSpec) error {
	for i := range specs {
		s := specs[i]
		if strings.TrimSpace(s.Name) != mediaViewToolName {
			continue
		}
		if s.Capability != mediaViewCapability || s.ResourceValue != mediaViewResourceValue || s.Egress != mediaViewEgress ||
			s.Mutation != "none" || s.Sandbox != nil || s.Modelo != nil {
			return ErrMediaNoTurnoSemTool
		}
		return nil
	}
	return ErrMediaNoTurnoSemTool
}

// fechar liberta a pasta. Idempotente e nil-safe.
func (l *ligacaoMedia) fechar() {
	if l != nil && l.root != nil {
		_ = l.root.Close()
	}
}

// opcaoDoAdaptador é a opção do adaptador do gateway com o resolvedor. nil-safe: sem ligação,
// nenhuma opção (o adaptador recusa um turno com media).
func (l *ligacaoMedia) opcaoDoAdaptador() []opcaoDoAdaptador {
	if l == nil {
		return nil
	}
	return []opcaoDoAdaptador{modelgateway.WithMediaResolver(l.resolver)}
}

// lerParte lê a imagem `doc` da pasta com as regras das tools de modelo e devolve a parte, ou a
// causa da recusa. O tipo sai dos primeiros bytes, nunca da extensão.
func (l *ligacaoMedia) lerParte(doc string) (port.MediaPart, string) {
	dados, causa := lerConfinado(l.raiz, doc, port.MaxImageBytes)
	if causa != "" {
		return port.MediaPart{}, causa
	}
	for _, tipo := range []string{port.ImageMediaPNG, port.ImageMediaJPEG} {
		parte := port.MediaPart{MediaType: tipo, Data: dados}
		if parte.Validate() == nil {
			return parte, ""
		}
	}
	return port.MediaPart{}, ocrCausaFormato
}

// resolver é o [modelgateway.MediaResolver]: lê de novo o documento da referência. O adaptador
// confere o digest, o tipo e o tamanho contra ela.
func (l *ligacaoMedia) resolver(ref agentruntime.MediaRef) (port.MediaPart, error) {
	parte, causa := l.lerParte(ref.Doc)
	if causa != "" {
		l.falhadas.Add(1)
		return port.MediaPart{}, fmt.Errorf("aos: media %s ilegivel: %s", mediaViewToolName, causa)
	}
	l.resolvidas.Add(1)
	return parte, nil
}

// entradaMediaView é o input da tool, tal como o modelo o pede.
type entradaMediaView struct {
	DocID string `json:"doc_id"`
}

// executar é a [referencemonitor.ToolFunc] da `media_view`. Só o despacho do RM a chama, depois do
// permit. Devolve a referência, ou um erro com a causa (vocabulário das tools de modelo).
func (l *ligacaoMedia) executar(_ context.Context, input []byte) ([]byte, error) {
	dec := json.NewDecoder(bytes.NewReader(input))
	dec.DisallowUnknownFields()
	var in entradaMediaView
	if err := dec.Decode(&in); err != nil || in.DocID == "" {
		return l.recusar(ocrCausaEntrada)
	}
	// Um documento que a referência não pode levar (fora do alfabeto dos rótulos, ou longo demais) é
	// recusado aqui: senão a tool respondia com sucesso e nenhum turno levava a imagem.
	if !agentruntime.ValidMediaDoc(in.DocID) {
		return l.recusar(ocrCausaEntrada)
	}
	if _, err := dec.Token(); err != io.EOF {
		return l.recusar(ocrCausaEntrada)
	}
	parte, causa := l.lerParte(in.DocID)
	if causa != "" {
		return l.recusar(causa)
	}
	ref := agentruntime.MediaRef{Doc: in.DocID, SHA256: parte.Digest(), MediaType: parte.MediaType, Bytes: int64(len(parte.Data))}
	out, err := ref.Marshal()
	if err != nil {
		return l.recusar(ocrCausaEntrada)
	}
	l.lidas.Add(1)
	return out, nil
}

// recusar conta a recusa e devolve o erro da tool com a causa.
func (l *ligacaoMedia) recusar(causa string) ([]byte, error) {
	l.recusadas.Add(1)
	return nil, fmt.Errorf("%s: %s", mediaViewToolName, causa)
}

// semMediaView tira a `media_view` da lista das tools oferecidas sem executor: ligada, o executor
// dela é código do nó, registado no RM pelo Bootstrap. Desligada, continua órfã — e é declarada.
func semMediaView(semExecutor []string, ligada bool) []string {
	if !ligada {
		return semExecutor
	}
	var out []string
	for _, n := range semExecutor {
		if n != mediaViewToolName {
			out = append(out, n)
		}
	}
	return out
}

// mediaNoTurnoBanner é a linha de arranque. Sem ligação não sai linha nenhuma.
func mediaNoTurnoBanner(l *ligacaoMedia) []string {
	if l == nil {
		return nil
	}
	return []string{fmt.Sprintf("media no turno (AOS-546, ADR-043 §2.2): AOS_MEDIA_NO_TURNO=on — os runs NOVOS ficam no layout %s; a tool %s le uma imagem PNG ou JPEG (ate %d bytes) do nivel de topo de %s=%q e devolve uma REFERENCIA (documento, sha256, tipo, tamanho), sem bytes e sem egress; no turno seguinte o adaptador resolve a imagem pela mesma pasta, confere o digest e envia-a ao MODELO DO RUN, so se o perfil da rota declarar a capacidade imagem (senao o gateway recusa e o run falha). Resultado e segmento untrusted. Retoma sem o ficheiro: o turno falha (media_ilegivel). Recuo: off",
		agentruntime.AssemblyVersion170, mediaViewToolName, port.MaxImageBytes, ocrSeedDirEnv, l.pasta)}
}
