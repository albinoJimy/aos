package main

// classe_de_media.go — O EFEITO DE UMA TOOL DE MODELO, ESCRITO UMA SÓ VEZ (AOS-566, fatia 1).
//
// A `ocr_read` (ocr_tool.go) e a `video_read` (video_tool.go) faziam, cada uma no seu ficheiro, a
// MESMA sequência: descodificar a entrada, exigir a credencial NHI antes de tocar no ficheiro, ler
// a media, reservar no tecto do run e chamar o gateway. Duas cópias da mesma ordem são dois sítios
// onde a ordem pode divergir — e a ordem É a propriedade (a identidade antes da leitura, a reserva
// antes do pedido: ADR-042 §2.2 e §2.6). Aqui a sequência existe uma vez, em [executarDeModelo], e
// o que cada tool tem de seu entra por [classeDeMedia].
//
// O QUE É DE CADA TOOL são quatro coisas, e são exactamente os campos de [classeDeMedia]: o schema
// do input, a instrução fixa com o `max_tokens` e o nome do resultado de sucesso, e a preparação da
// media — a leitura, a reserva desta chamada e a leitura da resposta ([pedidoDeMedia]).
//
// A MENSAGEM DEIXOU DE SER ESPECÍFICA. A parte da media vai numa [port.MediaPart] e entra na
// mensagem por [port.Message.AttachMedia] (AOS-546, ADR-043 §2.2), que a põe no campo da CLASSE
// dela — `Images` ou `Videos` — por uma tabela única na porta. Nenhum `switch` por modalidade vive
// aqui: uma modalidade nova é uma linha nessa tabela, não um ramo neste ficheiro.
//
// A FATIA 2 pôs o USO em configuração e deixou o EFEITO em Go. Até aqui o nó decidia por NOME: o
// arranque recusava o bloco `modelo` a qualquer entrada que não se chamasse `ocr_read` ou
// `video_read`, e a composição procurava essas duas entradas pelo nome exacto. O nome é o critério
// errado — duas instalações que queiram a mesma leitura de imagem com outro alias, outros limites
// ou outra descrição teriam de pedir código novo ao nó. O que decide passa a ser a CLASSE DE MEDIA
// que o bloco declara ([perfilDaClasse], vocabulário FECHADO): ela escolhe o efeito, que continua a
// ser código, e o registo escolhe o nome da tool, o alias e os limites. É a emenda do ADR-043 §6 ao
// ADR-042 §2.1 — «o efeito continua a ser código (uma implementação), e só o uso passa a
// configuração».
//
// [ligacaoOCR] e [ligacaoVideo] continuam a ser duas structs: são as duas CONFIGURAÇÕES desta
// máquina que o nó traz em código, uma por classe.

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	budget "github.com/aos-ref/control-plane/budget"
	referencemonitor "github.com/aos-ref/kernel/reference-monitor"
	"github.com/aos-ref/platform/model-gateway/policy/allowlist"
	"github.com/aos-ref/platform/model-gateway/port"
)

// classeDeMedia é o que distingue uma tool de modelo da outra no efeito da tool call. É dado, não
// comportamento: cada `executar` declara-o e a sequência fica em [executarDeModelo].
type classeDeMedia struct {
	// schema é a forma do input DEPOIS da reescrita do efeito ([newEfeitoDeModeloRewriter]): o que
	// a entrada tem de declarar para ser desta tool.
	schema string
	// sistema e pedido são a instrução fixa e versionada da tool — a mensagem `system` e o texto da
	// mensagem `user` que leva a media.
	sistema, pedido string
	// maxTokens é o `max_tokens` da chamada e sucesso o nome do resultado quando a tool aceita a
	// resposta (`transcrito`, `descrito`).
	maxTokens int
	sucesso   string
	// preparar lê a media do `doc_id` e apura o que a chamada dela precisa, ou devolve a causa da
	// recusa (vocabulário fechado da tool). Nada do que ela faz chama o modelo nem custa dinheiro.
	preparar func(ctx context.Context, in entradaOCR) (pedidoDeMedia, string)
}

// pedidoDeMedia é o que a leitura de uma media apura para a chamada dela: a parte que vai na
// mensagem, quanto reservar no tecto do run, o que uma resposta NÃO MEDIDA cobra e como se lê a
// resposta. Os factos da media que a leitura apurou — o formato de uma imagem, a duração de um
// vídeo — ficam fechados em `interpretar`, que é o único sítio que os volta a precisar.
type pedidoDeMedia struct {
	parte       port.MediaPart
	maximo      budget.Amount
	cobrar      tectoDaChamada
	interpretar func(resp port.ChatResponse, servido string) ([]byte, string)
}

// executarDeModelo é a [referencemonitor.CostingToolFunc] de uma tool de modelo, menos a classe da
// media: a sequência que a `ocr_read` e a `video_read` partilham, pela ordem que é a propriedade.
// Só o despacho do RM lá chega, depois do permit.
func executarDeModelo(ctx context.Context, l *ligacaoDeModelo, c classeDeMedia, input []byte) ([]byte, int64, error) {
	var in entradaOCR
	if err := json.Unmarshal(input, &in); err != nil || in.Schema != c.schema || in.DocID == "" || in.RunID == "" || in.StepID == "" {
		return l.falhar(ocrCausaEntrada, 0)
	}
	// A IDENTIDADE ANTES DE TUDO: sem o token do run não se lê o ficheiro (ADR-042 §2.2).
	credencial := modelCredentialFromContext(ctx)
	if credencial == "" {
		return l.falhar(ocrCausaSemNHI, 0)
	}
	pedido, causa := c.preparar(ctx, in)
	if causa != "" {
		return l.falhar(causa, 0)
	}
	// A MENSAGEM MONTA-SE ANTES DA RESERVA, e isso é de propósito: uma recusa depois de reservar
	// deixaria a reserva por saldar — quem a liberta é o `defer` de [ligacaoDeModelo.chamar], e aqui
	// ainda não se lá chegou. Montar primeiro mantém o caminho de recusa sem efeito no tecto do run.
	//
	// O ERRO DE [port.Message.AttachMedia] É INALCANÇÁVEL pelas duas tools de hoje — cada uma já
	// validou a parte na leitura, e a do espaço de handles traz o tipo que a porta do AOS-561
	// conferiu PELOS BYTES antes de o run abrir —, mas não se ignora: fail-closed, com a causa de
	// formato, que é a mesma que a leitura daria.
	utilizador := port.Message{Role: port.RoleUser, Content: c.pedido}
	if err := utilizador.AttachMedia(pedido.parte); err != nil {
		return l.falhar(ocrCausaFormato, 0)
	}
	// A RESERVA (ADR-042 §2.6): com a media lida e antes do pedido, o máximo DESTA chamada no tecto
	// do run. Não cabe ⇒ negada sem chamar o modelo.
	reserva, causa := l.reservar(ctx, in.RunID, pedido.maximo)
	if causa != "" {
		return l.falhar(causa, 0)
	}
	return l.chamar(ctx, in, credencial, reserva, pedido.cobrar,
		[]port.Message{{Role: port.RoleSystem, Content: c.sistema}, utilizador},
		c.maxTokens, c.sucesso, pedido.interpretar)
}

// toolDeModelo é uma tool de modelo COMPOSTA, qualquer que seja a classe dela: o que o nó precisa
// de lhe pedir sem saber qual é. Tudo o que é igual em todas vive na parte comum ([comum]); o
// efeito da tool call e a linha do arranque são da classe.
type toolDeModelo interface {
	// comum é a parte partilhada da ligação: o nome, a classe, o alias, a pasta, o gateway, a
	// métrica, o custo e o vocabulário de erro fechado.
	comum() *ligacaoDeModelo
	// executar é a [referencemonitor.CostingToolFunc] que o RM despacha depois do permit.
	executar(ctx context.Context, input []byte) ([]byte, int64, error)
	// linhaDoArranque declara a tool ligada no log do nó.
	linhaDoArranque() string
}

// perfilDaClasse é o que o NÓ traz EM CÓDIGO para uma classe de media: o efeito e os valores por
// omissão dos limites. Uma classe nova é uma entrada em [classesDeMedia] com o efeito dela — não
// um nome de tool numa lista.
type perfilDaClasse struct {
	// schema é a forma do input DEPOIS da reescrita do efeito ([newEfeitoDeModeloRewriter]).
	schema string
	// maxTokens e prazo são os limites POR OMISSÃO — os que o bloco `modelo` usa quando não os
	// declara, e por isso os valores com que a instrução da classe foi medida.
	maxTokens int
	prazo     time.Duration
	// semGateway é o erro de arranque de uma tool desta classe declarada no registo e sem o Model
	// Gateway composto (o efeito dela é uma chamada a ESSE gateway e não há outro caminho).
	semGateway error
	// compor confere as condições de arranque e monta a ligação, sem o gateway.
	compor func(lim limitesDaTool, spec *modelToolSpec, assinados *perfisAssinados, externa *allowlist.Policy, region, board string, comCusto bool) (toolDeModelo, error)
}

// classesDeMedia é o VOCABULÁRIO FECHADO das classes que o bloco `modelo` pode declarar. As chaves
// são as da porta do gateway ([port.MediaClassImage], [port.MediaClassVideo]) e não um segundo
// vocabulário com os mesmos dois nomes: é a mesma classe que [port.Message.AttachMedia] usa para
// pôr a parte no campo certo do pedido.
var classesDeMedia = map[string]perfilDaClasse{
	port.MediaClassImage: {
		schema:     ocrInputSchema,
		maxTokens:  ocrMaxTokens,
		prazo:      ocrPrazo,
		semGateway: ErrOCRSemGateway,
		compor:     comporLigacaoDeImagem,
	},
	port.MediaClassVideo: {
		schema:     videoInputSchema,
		maxTokens:  videoMaxTokens,
		prazo:      videoPrazo,
		semGateway: ErrVideoSemGateway,
		compor:     comporLigacaoDeVideo,
	},
}

// classePorOmissao é a PONTE DE COMPATIBILIDADE com os manifestos escritos antes de a classe se
// declarar: as duas tools que o nó trazia por nome, e só elas. Um manifesto de produção que não
// mude continua a carregar; qualquer outro nome TEM de declarar a classe, e é por isso que esta
// ponte não é um buraco — ela não alarga nada, apenas não parte o que já existia.
var classePorOmissao = map[string]string{
	ocrToolName:   port.MediaClassImage,
	videoToolName: port.MediaClassVideo,
}

// prazoMaximoDeTool é o tecto do prazo que uma entrada do registo pode declarar. Existe porque um
// prazo é o único destes limites cujo erro de escrita PRENDE um run: um `prazo_s` com um zero a
// mais mantém a tool call aberta durante horas, e o run com ela. Um `max_tokens` grande de mais só
// torna a reserva maior e a chamada mais fácil de NEGAR — o lado seguro —, e por isso não tem tecto.
const prazoMaximoDeTool = 10 * time.Minute

// limitesDaTool é a identidade e os limites EFECTIVOS de uma tool de modelo: o que o bloco `modelo`
// declarou, com o da classe onde o campo faltar.
type limitesDaTool struct {
	nome, alias, classe string
	maxTokens           int
	prazo               time.Duration
}

// limitesDoBlocoModelo resolve a classe e os limites de uma entrada com bloco `modelo`. Fail-closed
// em todas as direcções: uma classe fora do vocabulário, uma classe em falta numa tool que a ponte
// de compatibilidade não cobre, ou um limite não-positivo ou acima do tecto ABORTAM o arranque, em
// vez de caírem em silêncio no valor da classe — um campo que o operador julga ter declarado e que
// o nó ignora é a pior das duas saídas.
func limitesDoBlocoModelo(s modelToolSpec) (limitesDaTool, perfilDaClasse, error) {
	nome := strings.TrimSpace(s.Name)
	lim := limitesDaTool{nome: nome, alias: strings.TrimSpace(s.Modelo.Alias)}
	lim.classe = strings.ToLower(strings.TrimSpace(s.Modelo.Classe))
	if lim.classe == "" {
		lim.classe = classePorOmissao[nome]
	}
	if lim.classe == "" {
		return limitesDaTool{}, perfilDaClasse{}, fmt.Errorf("%w: tool %q tem o bloco `modelo` e nao declara `classe` — a classe de media escolhe o EFEITO que o no traz em codigo (%s); so as tools %q e %q a dispensam, por serem anteriores ao campo", ErrBadModelTools, nome, strings.Join(nomesDasClasses(), "|"), ocrToolName, videoToolName)
	}
	perfil, conhecida := classesDeMedia[lim.classe]
	if !conhecida {
		return limitesDaTool{}, perfilDaClasse{}, fmt.Errorf("%w: tool %q declara `modelo.classe` %q, fora do vocabulario fechado (%s)", ErrBadModelTools, nome, s.Modelo.Classe, strings.Join(nomesDasClasses(), "|"))
	}
	lim.maxTokens, lim.prazo = perfil.maxTokens, perfil.prazo
	if n := s.Modelo.MaxTokens; n != 0 {
		if n < 0 {
			return limitesDaTool{}, perfilDaClasse{}, fmt.Errorf("%w: tool %q declara `modelo.max_tokens` %d — tem de ser positivo", ErrBadModelTools, nome, n)
		}
		lim.maxTokens = n
	}
	if n := s.Modelo.PrazoSegundos; n != 0 {
		p := time.Duration(n) * time.Second
		if n < 0 || p > prazoMaximoDeTool {
			return limitesDaTool{}, perfilDaClasse{}, fmt.Errorf("%w: tool %q declara `modelo.prazo_s` %d — tem de ser positivo e nao passar de %s", ErrBadModelTools, nome, n, prazoMaximoDeTool)
		}
		lim.prazo = p
	}
	return lim, perfil, nil
}

// nomesDasClasses são as classes do vocabulário, por ordem, para as mensagens de recusa.
func nomesDasClasses() []string {
	nomes := make([]string, 0, len(classesDeMedia))
	for c := range classesDeMedia {
		nomes = append(nomes, c)
	}
	sort.Strings(nomes)
	return nomes
}

// comporToolDeModelo compõe UMA ligação a partir de UMA entrada do registo com bloco `modelo`, pela
// CLASSE que ela declara. É o único sítio onde o nó decide que efeito uma entrada do registo tem.
func comporToolDeModelo(spec *modelToolSpec, assinados *perfisAssinados, externa *allowlist.Policy, region, board string, comCusto bool) (toolDeModelo, error) {
	lim, perfil, err := limitesDoBlocoModelo(*spec)
	if err != nil {
		return nil, err
	}
	return perfil.compor(lim, spec, assinados, externa, region, board, comCusto)
}

// specsDeModeloNoRegisto devolve as entradas do registo de tools do ambiente que têm bloco `modelo`
// — as que o nó compõe. Vazio ⇒ nenhuma.
func specsDeModeloNoRegisto() ([]modelToolSpec, error) {
	specs, err := readModelToolSpecs()
	if err != nil {
		return nil, err
	}
	var com []modelToolSpec
	for _, s := range specs {
		if s.Modelo != nil {
			com = append(com, s)
		}
	}
	return com, nil
}

// semGatewayDaClasse é o erro de arranque de uma tool declarada no registo e sem gateway composto.
func semGatewayDaClasse(classe string) error {
	if p, ok := classesDeMedia[classe]; ok {
		return p.semGateway
	}
	return ErrOCRSemGateway
}

// reescritaDaToolDeModelo é a reescrita do efeito de uma tool composta: o schema do input é o da
// CLASSE dela, e o nome é o que o registo lhe deu.
func reescritaDaToolDeModelo(t toolDeModelo) func(referencemonitor.Call) (referencemonitor.Call, error) {
	c := t.comum()
	return newEfeitoDeModeloRewriter(c.tool, classesDeMedia[c.classe].schema)
}

// registarToolDeModelo regista uma tool de modelo no RM do nó com RegisterCosting. Corre no
// Bootstrap, DEPOIS de o runtime seguro existir. Fail-closed: sem o gateway ligado, ou com o
// registo recusado, o arranque aborta.
func registarToolDeModelo(sec interface {
	RegisterCosting(string, referencemonitor.CostingToolFunc) error
}, t toolDeModelo, log func(string, ...any)) error {
	c := t.comum()
	if c.gw == nil {
		return semGatewayDaClasse(c.classe)
	}
	if err := sec.RegisterCosting(c.tool, t.executar); err != nil {
		return fmt.Errorf("aos: registar a tool %s: %w", c.tool, err)
	}
	log("%s", t.linhaDoArranque())
	return nil
}
