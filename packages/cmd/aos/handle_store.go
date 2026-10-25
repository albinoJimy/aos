package main

// O ESPAÇO PRÓPRIO DE HANDLES (AOS-550, fase R2 do ADR-043) — `AOS_HANDLE_DIR`.
//
// O conteúdo untrusted que viaja por handle vive AQUI, e não na via da `artifact_write`. É a
// correcção central da revisão do desenho, e a razão é medida, não estética:
//
//   - os tectos do armazém de artefactos são 16 artefactos e 16 MiB POR RUN
//     ([artifactMaxPorRun], [artifactMaxBytesPorRun]) e o orçamento é PARTILHADO com o que o
//     modelo grava: um run que lesse 14 coisas só podia gravar 2 ficheiros;
//   - o vocabulário de tipos de lá é FECHADO e o [validarConteudo] recusa binário e UTF-8
//     inválido: um resultado de tool binário não se guardava, logo não havia handle, logo o run
//     falhava;
//   - com o MESMO emissor nos eventos ([artifactNHI]), todo o conteúdo em quarentena passaria a
//     aparecer em `GET /runs/{id}/artifacts` e a ser descarregável com `read:artifact` — conteúdo
//     que hoje só a via de trajectória expõe mudava de capability de leitura sem ninguém o
//     declarar;
//   - e a escrita do runtime NÃO passa pelo RM, pelo que a regra assinada `allow_artifact_write`
//     deixaria de governar todas as escritas que governa hoje.
//
// Logo: espaço próprio, com os seus TECTOS, VOCABULÁRIO ABERTO e SUPERFÍCIE DE LEITURA PRÓPRIA,
// reutilizando a MAQUINARIA de selagem — o [contentSealer] por titular, o mesmo das capturas — e
// não o CAMINHO da tool.
//
// # A SUPERFÍCIE DE LEITURA É INTERNA, E É UMA DECISÃO
//
// Não nasce rota HTTP nenhuma para o CONTEÚDO deste espaço, e o emissor dos eventos é um sub-papel
// PRÓPRIO ([handleNHI]), distinto do do armazém de artefactos: por isso o [artefactosDoRun] não vê
// estes eventos e nenhum conteúdo em quarentena se torna descarregável com `read:artifact`. A única
// leitura do conteúdo é a resolução interna do nó, pelo `CallRewriter`. Uma tool `artifact_read`
// continua explicitamente fora de âmbito: daria ao modelo um pedido de leitura por digest, que é o
// vector que o guarda do stream existe para fechar.
//
// OS METADADOS, SIM, TÊM SUPERFÍCIE (declarado): o payload do `handle.created` — token, digest,
// tipo, bytes, nome do nó, passo e titular selado — sai na trajectória do run, que serve os eventos
// do stream como estão, exactamente como já saem os do `artifact.created`. Nenhum byte do conteúdo,
// mas é superfície nova de metadados e não se finge que não é.
//
// # SELAGEM COM DADOS ASSOCIADOS DO RUN
//
// O `abrir` do armazém de artefactos reconfere o DIGEST, o que amarra o cifrado ao conteúdo mas
// NÃO ao run: um blob selado sob o mesmo titular, copiado para a pasta de outro run, abre e passa
// (resíduo 9 do ADR-034). Aqui o run vai AUTENTICADO com os bytes: o que se sela é
// `"handle:" + run_id + 0x00 + conteúdo`, e a abertura exige esse prefixo exacto antes de devolver
// um byte.
//
// PORQUE DENTRO DO PLAINTEXT E NÃO COMO AAD DO AEAD: o [audit.SealContent] sela com AAD `nil` e
// não tem parâmetro por onde a passar; dar-lhe um mudaria a porta partilhada por todos os
// chamadores (capturas, step-ledger) e os blobs já selados. A propriedade obtida é a mesma — o
// GCM autentica o plaintext inteiro, logo o prefixo não se altera sem a chave —, e fica num sítio
// onde não há blobs antigos para migrar.

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"unicode/utf8"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	"github.com/aos-ref/substrate/eventstore"
)

// EventTypeHandleCreated é o facto durável de um conteúdo selado em quarentena. É o guarda (b):
// um digest que nenhum evento do stream do run declara não se resolve.
const EventTypeHandleCreated = "handle.created"

// handleNHI é a identidade emissora destes factos: um sub-papel PRÓPRIO do nó, distinto do
// [artifactNHI]. É o que mantém os dois espaços separados na leitura — ver o cabeçalho.
const handleNHI = "nhi:aos-node/handle"

// handleStepPrefix é o prefixo do `step_id` do envelope: sem ele o evento colidiria com a
// `tool.call.mediated` do mesmo passo na chave de idempotência e era engolido como duplicado. O
// ordinal entra no sufixo porque um passo pode selar mais de um handle.
const handleStepPrefix = "handle-"

// Tectos PRÓPRIOS deste espaço. Não são os do armazém de artefactos, e não partilham orçamento
// com o que o modelo grava.
const (
	// handleMaxBytes é o tecto por handle: o tecto do corpo de um `tool_result`
	// ([agentruntime.MaxToolResultBytes], 256 KiB). É o mesmo número de propósito — este espaço
	// existe para guardar corpos de resultados.
	handleMaxBytes = agentruntime.MaxToolResultBytes
	// handleMaxPorRun é o nº de handles por run. 64 cobre um run que leia muito (o tecto de
	// artefactos do modelo é 16 e é outro orçamento).
	handleMaxPorRun = 64
	// handleMaxBytesPorRun é o tecto em bytes por run.
	handleMaxBytesPorRun = 16 << 20
	// handleMaxBytesOmissao é o tecto do volume sem AOS_HANDLE_MAX_BYTES.
	handleMaxBytesOmissao = 1 << 30
)

// O resultado de uma operação sobre um handle. Vocabulário FECHADO, que é também o dos rótulos da
// métrica: nenhum valor leva um byte do conteúdo, um token nem um digest.
//
// UM SÓ VOCABULÁRIO para a selagem e para a resolução, de propósito: dois — um no espaço, outro no
// plano — divergiriam no PR seguinte, e o operador que lê o `/metrics` não tem por que saber de que
// lado do código a recusa nasceu.
const (
	// Os dois resultados de sucesso.
	hSelado    = "selado"
	hResolvido = "resolvido"
	// As causas da SELAGEM.
	hCausaVazio   = "handle_vazio"
	hCausaRun     = "tecto_de_handles_do_run"
	hCausaVolume  = "volume_de_handles_cheio"
	hCausaCifra   = "cifra_indisponivel"
	hCausaDisco   = "disco_indisponivel"
	hCausaRegisto = "registo_indisponivel"
	// hCausaGrande vale nos DOIS sentidos: um corpo acima do tecto não entra em quarentena nem
	// sai dela. É o tecto do corpo de um `tool_result` ([agentruntime.MaxToolResultBytes]).
	hCausaGrande = "handle_acima_do_tecto"
	// hCausaSequencia — o stream do run tem um `handle.created` ilegível, sem token ou com o
	// ordinal fora da posição, e a leitura dos handles PAROU ali. Os handles seguintes desse run
	// deixam de existir (os eventos são imutáveis) e toda a resolução passa a recusar. É
	// fail-closed, e o que este contador acrescenta é deixar de ser silencioso: sem ele, o
	// operador via `handle_nao_reconhecido` em cada call e nada que dissesse porquê.
	hCausaSequencia = "sequencia_de_handles_inconsistente"
)

// handleResultados é o vocabulário fechado, pela ordem em que sai no `/metrics`.
func handleResultados() []string {
	return []string{
		hSelado, hResolvido,
		hCausaVazio, hCausaGrande, hCausaRun, hCausaVolume, hCausaCifra, hCausaDisco, hCausaRegisto,
		hCausaSequencia, handleCausaDesconhecido, handleCausaSemEvento, handleCausaNaoTextual, handleCausaAninhado,
		handleCausaParcial, handleCausaSlot, handleCausaArgs, handleCausaDuplicadas,
		handleCausaDemasiadas, handleCausaBytesDaCall, handleCausaSemRun, handleCausaIlegivel,
	}
}

var (
	// ErrBadHandleDir — AOS_HANDLE_DIR definida e a pasta não se abre.
	ErrBadHandleDir = errors.New("aos: AOS_HANDLE_DIR invalida — tem de ser uma pasta existente e gravavel no volume do no")
	// ErrBadHandleMaxBytes — AOS_HANDLE_MAX_BYTES fora de um inteiro positivo.
	ErrBadHandleMaxBytes = errors.New("aos: AOS_HANDLE_MAX_BYTES invalida — um inteiro positivo de bytes")
	// ErrHandleSemCifra — o espaço ligado sem o cifrador por titular: nunca bytes em claro.
	ErrHandleSemCifra = errors.New("aos: AOS_HANDLE_DIR exige o cifrador por titular do no (o conteudo em quarentena nunca fica em claro no disco)")
	// ErrHandleDirIgualAoDosArtefactos — AOS_HANDLE_DIR e AOS_ARTIFACT_DIR a apontar para a mesma
	// pasta. Os dois espaços nomeiam o ficheiro da mesma maneira (`sha256(run)/sha256(conteudo)`)
	// e selam plaintexts DIFERENTES: partilhar a pasta faz o segundo a gravar encontrar o ficheiro
	// do primeiro, dar sucesso sem escrever, e o artefacto passa a não bater com o digest em todas
	// as leituras. Medido na revisão de segurança do AOS-550 (achado M-3): a tool respondia
	// sucesso e o download dava 500, sem uma palavra no arranque.
	ErrHandleDirIgualAoDosArtefactos = errors.New("aos: AOS_HANDLE_DIR e AOS_ARTIFACT_DIR tem de ser pastas DIFERENTES — os dois espacos nomeiam os ficheiros da mesma maneira e selam conteudos diferentes; partilhar a pasta torna os artefactos ilegiveis")
	// ErrHandleOutroRun — o cifrado abriu mas foi selado para OUTRO run. É o guarda dos dados
	// associados: um blob copiado para a pasta deste run não passa.
	ErrHandleOutroRun = errors.New("aos: o conteudo em quarentena foi selado para outro run")
	// ErrHandleDigest — os bytes decifrados não batem com o digest pedido.
	ErrHandleDigest = errors.New("aos: o conteudo em quarentena nao bate com o digest")
)

// armazemDeHandles é o espaço de handles ligado.
type armazemDeHandles struct {
	root   *os.Root
	pasta  string
	maximo int64
	cifra  cifradorDeArtefactos
	// registar escreve o evento de proveniência (ligado no Bootstrap; nil ⇒ a selagem recusa).
	registar func(ctx context.Context, runID string, p handleCriado) error
	// declarados lê do stream do run os handles que os eventos declaram, pela ordem.
	declarados func(ctx context.Context, runID string) ([]handleCriado, error)

	mu    sync.Mutex // serializa as selagens (ordinal, tectos do run e do volume)
	usado atomic.Int64

	// contadores é um contador por valor de [handleResultados]. Sempre presente: o zero é um zero
	// verdadeiro, e é o que o `/metrics` publica.
	contadores map[string]*atomic.Int64
}

// conta soma um resultado, e devolve-o para o chamador o usar como causa. Um valor fora do
// vocabulário não é contado — e não existe, porque o vocabulário é fechado e conferido em teste.
func (a *armazemDeHandles) conta(resultado string) string {
	if c := a.contadores[resultado]; c != nil {
		c.Add(1)
	}
	return resultado
}

// lido devolve o contador de um resultado.
func (a *armazemDeHandles) lido(resultado string) int64 {
	if c := a.contadores[resultado]; c != nil {
		return c.Load()
	}
	return 0
}

// handleCriado é o corpo do evento [EventTypeHandleCreated]. Sem o conteúdo, e sem um byte de
// texto escolhido por quem o produziu: o `Name` é o nome cunhado pelo nó.
type handleCriado struct {
	Token         string `json:"token"`
	Digest        string `json:"digest"`
	MediaType     string `json:"media_type"`
	Bytes         int    `json:"bytes"`
	Name          string `json:"name"`
	WellFormed    bool   `json:"well_formed"`
	Tool          string `json:"tool"`
	StepID        string `json:"step_id"`
	SealedSubject string `json:"sealed_subject"`
}

// handleConfigFromEnv lê AOS_HANDLE_DIR e AOS_HANDLE_MAX_BYTES. Pasta vazia ⇒ "" (desligado).
func handleConfigFromEnv() (string, int64, error) {
	pasta := strings.TrimSpace(os.Getenv("AOS_HANDLE_DIR"))
	maximo := int64(handleMaxBytesOmissao)
	if raw := strings.TrimSpace(os.Getenv("AOS_HANDLE_MAX_BYTES")); raw != "" {
		n, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || n <= 0 {
			return "", 0, fmt.Errorf("%w (veio %q)", ErrBadHandleMaxBytes, raw)
		}
		maximo = n
	}
	return pasta, maximo, nil
}

// abrirArmazemDeHandles abre a pasta, confinada, e soma o que lá está para o tecto do volume. É o
// molde de [abrirArmazemDeArtefactos], e a duplicação é deliberada: os dois espaços têm tectos,
// vocabulário e superfície de leitura diferentes, e juntá-los era o defeito que a revisão fechou.
//
// `pastaDosArtefactos` entra aqui só para ser RECUSADA se for a mesma — ver
// [ErrHandleDirIgualAoDosArtefactos].
func abrirArmazemDeHandles(pasta, pastaDosArtefactos string, maximo int64, cifra cifradorDeArtefactos) (*armazemDeHandles, error) {
	if pasta == "" {
		return nil, nil
	}
	if cifra == nil {
		return nil, ErrHandleSemCifra
	}
	if igual, err := mesmaPasta(pasta, pastaDosArtefactos); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrBadHandleDir, err)
	} else if igual {
		return nil, ErrHandleDirIgualAoDosArtefactos
	}
	root, err := os.OpenRoot(pasta)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrBadHandleDir, err)
	}
	if maximo <= 0 {
		maximo = handleMaxBytesOmissao
	}
	a := &armazemDeHandles{root: root, pasta: pasta, maximo: maximo, cifra: cifra,
		contadores: make(map[string]*atomic.Int64, len(handleResultados()))}
	for _, r := range handleResultados() {
		a.contadores[r] = &atomic.Int64{}
	}
	var total int64
	_ = fs.WalkDir(root.FS(), ".", func(_ string, d fs.DirEntry, err error) error {
		if err == nil && d.Type().IsRegular() && !strings.HasSuffix(d.Name(), ".tmp") {
			if info, ierr := d.Info(); ierr == nil {
				total += info.Size()
			}
		}
		return nil
	})
	a.usado.Store(total)
	return a, nil
}

// mesmaPasta diz se dois caminhos designam a mesma pasta. Compara o caminho RESOLVIDO (absoluto e
// com as ligações simbólicas seguidas): dois caminhos escritos de forma diferente podem ser a
// mesma pasta, e é a pasta que importa. Com o segundo vazio, nunca são a mesma.
func mesmaPasta(a, b string) (bool, error) {
	if strings.TrimSpace(b) == "" {
		return false, nil
	}
	resolver := func(p string) (string, error) {
		abs, err := filepath.Abs(p)
		if err != nil {
			return "", err
		}
		// Se o caminho não existir, o EvalSymlinks falha; o absoluto já serve para comparar.
		if real, err := filepath.EvalSymlinks(abs); err == nil {
			return real, nil
		}
		return abs, nil
	}
	ra, err := resolver(a)
	if err != nil {
		return false, err
	}
	rb, err := resolver(b)
	if err != nil {
		return false, err
	}
	return filepath.Clean(ra) == filepath.Clean(rb), nil
}

// fechar liberta a pasta. Idempotente e nil-safe.
func (a *armazemDeHandles) fechar() {
	if a != nil && a.root != nil {
		_ = a.root.Close()
	}
}

// ligacaoDoRun são os DADOS ASSOCIADOS que vão autenticados com os bytes: `handle:` e o SHA-256 do
// run em hexadecimal. Ver o cabeçalho deste ficheiro.
//
// PORQUE O DIGEST DO RUN E NÃO O RUN EM CLARO: a ligação tem de ser AUTO-DELIMITADA. Com o run em
// claro e um separador, `handle:run-1` não é prefixo de `handle:run-10` só porque o separador lá
// está — e o separador só é seguro enquanto outra camada garantir que um `run_id` não o contém
// (hoje o [eventstore] recusa caracteres de controlo, mas essa é uma invariante de outro ficheiro).
// Com 64 hexadecimais de comprimento fixo, nenhuma ligação é prefixo de outra, e a propriedade não
// depende de ninguém (achado B-5 da revisão de segurança).
func ligacaoDoRun(runID string) []byte {
	return []byte("handle:" + pastaDoRun(runID))
}

// selar cifra e grava um conteúdo em quarentena, cunha o token e sela a proveniência. Devolve o
// token, o digest e os factos que o nó cunhou — ou a causa da recusa.
//
// A ORDEM NÃO É ARBITRÁRIA: os tectos primeiro (a recusa mais barata), depois o ordinal lido do
// stream (sob o mutex, pelo que duas selagens do mesmo run não disputam o mesmo número), depois a
// cifra, o disco e só por fim o evento. Sem evento não há handle: o guarda (b) recusaria o que
// ficasse no disco, e o contador conta a recusa.
func (a *armazemDeHandles) selar(ctx context.Context, subject, runID, stepID, tool, tipo string, conteudo []byte) (handleCriado, string) {
	switch {
	case len(conteudo) == 0:
		return handleCriado{}, a.conta(hCausaVazio)
	case len(conteudo) > handleMaxBytes:
		return handleCriado{}, a.conta(hCausaGrande)
	}
	if a.registar == nil || a.declarados == nil {
		return handleCriado{}, a.conta(hCausaRegisto)
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	anteriores, err := a.declarados(ctx, runID)
	if err != nil {
		return handleCriado{}, a.conta(hCausaRegisto)
	}
	if len(anteriores) >= handleMaxPorRun {
		return handleCriado{}, a.conta(hCausaRun)
	}
	var bytesDoRun int
	for _, h := range anteriores {
		bytesDoRun += h.Bytes
	}
	if bytesDoRun+len(conteudo) > handleMaxBytesPorRun {
		return handleCriado{}, a.conta(hCausaRun)
	}
	ordinal := len(anteriores) + 1
	hexd := digestHex(conteudo)
	p := handleCriado{
		Token:         agentruntime.MintHandleToken(runID, "sha256:"+hexd, ordinal),
		Digest:        "sha256:" + hexd,
		MediaType:     tipo,
		Bytes:         len(conteudo),
		Name:          tool + "-" + strconv.Itoa(ordinal),
		WellFormed:    formaBemFormada(tipo, conteudo),
		Tool:          tool,
		StepID:        stepID,
		SealedSubject: subject,
	}
	if causa := a.gravar(ctx, subject, runID, hexd, conteudo); causa != "" {
		return handleCriado{}, a.conta(causa)
	}
	if err := a.registar(ctx, runID, p); err != nil {
		// SEM EVENTO NÃO HÁ HANDLE: o guarda (b) recusaria este conteúdo para sempre, e deixá-lo
		// no disco seria ocupar o tecto do volume com um ficheiro que ninguém consegue resolver
		// nem apagar (achado M-4 da revisão de segurança). Desfaz-se a gravação.
		a.apagar(runID, hexd)
		return handleCriado{}, a.conta(hCausaRegisto)
	}
	a.conta(hSelado)
	return p, ""
}

// gravar cifra (com os dados associados do run) e escreve o ficheiro, com o tecto do volume. O
// mesmo conteúdo no mesmo run reaproveita o ficheiro: o digest é o mesmo e os bytes também.
func (a *armazemDeHandles) gravar(ctx context.Context, subject, runID, hexd string, conteudo []byte) string {
	dir := pastaDoRun(runID)
	nome := dir + "/" + hexd
	if _, err := a.root.Stat(nome); err == nil {
		return ""
	}
	cifrado, err := a.cifra.SealContent(ctx, subject, runID, append(ligacaoDoRun(runID), conteudo...))
	if err != nil {
		return hCausaCifra
	}
	if a.usado.Load()+int64(len(cifrado)) > a.maximo {
		return hCausaVolume
	}
	if err := a.root.Mkdir(dir, 0o700); err != nil && !errors.Is(err, fs.ErrExist) {
		return hCausaDisco
	}
	tmp := nome + ".tmp"
	f, err := a.root.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return hCausaDisco
	}
	if _, werr := f.Write(cifrado); werr != nil {
		_ = f.Close()
		_ = a.root.Remove(tmp)
		return hCausaDisco
	}
	if err := f.Close(); err != nil {
		_ = a.root.Remove(tmp)
		return hCausaDisco
	}
	if err := a.root.Rename(tmp, nome); err != nil {
		_ = a.root.Remove(tmp)
		return hCausaDisco
	}
	a.usado.Add(int64(len(cifrado)))
	return ""
}

// apagar desfaz uma gravação cujo evento de proveniência não ficou durável. Best-effort: se o
// ficheiro não sair, o tecto do volume fica com ele — e é melhor do que não tentar.
func (a *armazemDeHandles) apagar(runID, hexd string) {
	nome := pastaDoRun(runID) + "/" + hexd
	info, err := a.root.Stat(nome)
	if err != nil {
		return
	}
	if err := a.root.Remove(nome); err == nil {
		a.usado.Add(-info.Size())
	}
}

// abrir lê e decifra um conteúdo em quarentena e confere TRÊS coisas antes de devolver um byte: o
// titular (a KEK), os dados associados do run (o blob foi selado para ESTE run) e o digest.
func (a *armazemDeHandles) abrir(ctx context.Context, subject, runID, digest string) ([]byte, error) {
	hexd, ok := strings.CutPrefix(digest, "sha256:")
	if !ok || len(hexd) != 64 {
		return nil, fs.ErrNotExist
	}
	f, err := a.root.Open(pastaDoRun(runID) + "/" + hexd)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	cifrado, err := io.ReadAll(io.LimitReader(f, 2*handleMaxBytes+4096))
	if err != nil {
		return nil, err
	}
	claro, err := a.cifra.OpenContent(ctx, subject, cifrado)
	if err != nil {
		return nil, err
	}
	ligacao := ligacaoDoRun(runID)
	if !bytes.HasPrefix(claro, ligacao) {
		return nil, ErrHandleOutroRun
	}
	corpo := claro[len(ligacao):]
	if digestHex(corpo) != hexd {
		return nil, ErrHandleDigest
	}
	return corpo, nil
}

// formaBemFormada é o facto de FORMA do handle: os bytes parseiam para o tipo declarado. É função
// PURA dos bytes e do tipo — sem relógio e sem estado de fora.
//
// O VOCABULÁRIO É ABERTO (ao contrário do armazém de artefactos): qualquer tipo é aceite, inclusive
// binário. Um tipo que o nó não saiba parsear fica `false` — o nó não afirma uma forma que não
// conferiu —, e isso não impede o handle de existir.
//
// ALCANCE REAL NESTA FASE, DECLARADO: o único produtor é a `artifact_write`, que valida ANTES de
// selar contra o vocabulário FECHADO dela e recusa binário; e na resolução um corpo que não seja
// UTF-8 é recusado, porque não cabe numa string JSON. A abertura existe para os produtores da R2/R3
// (um `tool_result` binário), e hoje nenhum conteúdo binário chega aqui pelo nó composto.
func formaBemFormada(tipo string, b []byte) bool {
	switch {
	case tipo == "application/json":
		return json.Valid(b)
	case tipo == "image/svg+xml":
		// XML BEM FORMADO, e não [svgSeguro]: a segurança de um SVG (sem script, sem ligações para
		// fora) é um predicado DIFERENTE da forma, e um SVG sintacticamente perfeito com um script
		// dentro é bem formado e inseguro. Dizer `well_formed=false` a esse punha no facto que o
		// planeador lê uma coisa que o nome não promete. Quem recusa o SVG inseguro é a
		// `artifact_write`, antes de chegar aqui.
		return xmlBemFormado(b)
	case tipo == "application/octet-stream":
		return true // qualquer sequência de bytes é uma sequência de bytes bem formada
	case strings.HasPrefix(tipo, "text/"):
		return utf8.Valid(b) && bytes.IndexByte(b, 0) < 0
	default:
		return false
	}
}

// xmlBemFormado diz se os bytes são um documento XML bem formado. Função pura dos bytes.
func xmlBemFormado(b []byte) bool {
	dec := xml.NewDecoder(bytes.NewReader(b))
	dec.Strict = true
	visto := false
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			return visto
		}
		if err != nil {
			return false
		}
		if _, ok := tok.(xml.StartElement); ok {
			visto = true
		}
	}
}

// registoDeHandle constrói a escrita durável do evento de proveniência. nil com store nil.
func registoDeHandle(es eventstore.EventStore) func(context.Context, string, handleCriado) error {
	if es == nil {
		return nil
	}
	return func(ctx context.Context, runID string, p handleCriado) error {
		if runID == "" || p.StepID == "" || p.Token == "" {
			return fmt.Errorf("aos: handle sem run_id/step_id/token (run=%q step=%q)", runID, p.StepID)
		}
		raw, err := json.Marshal(p)
		if err != nil {
			return err
		}
		ordinal, ok := agentruntime.HandleTokenOrdinal(p.Token)
		if !ok {
			return fmt.Errorf("aos: token de handle malformado")
		}
		_, err = es.Append(ctx, runID, eventstore.EventInput{
			Type:     EventTypeHandleCreated,
			Payload:  raw,
			RunID:    runID,
			StepID:   handleStepPrefix + p.StepID + "-" + strconv.Itoa(ordinal),
			Producer: eventstore.Producer{NHIID: handleNHI},
		})
		return err
	}
}

// handlesDoRun devolve os handles que os eventos do stream declaram, pela ordem dos eventos. É o
// guarda (b) na sua forma legível, e o molde é o [artefactosDoRun] — com o emissor PRÓPRIO deste
// espaço, que é o que impede os dois espaços de se verem um ao outro.
//
// Um evento ilegível, sem token ou com o token fora de sequência fica de fora e INTERROMPE a
// leitura: os tokens são função da posição, e devolver uma lista com um furo no meio daria a um
// handle o token de outro. Fail-closed — a quarentena desse run fica sem handles a partir dali.
//
// O segundo retorno é o nº de eventos deste espaço que a interrupção deixou de fora. Quem lê
// conta-o ([armazemDeHandles.declarados]) para a truncagem não ser silenciosa.
func handlesDoRun(events []eventstore.Event) ([]handleCriado, int) {
	var out []handleCriado
	parou := false
	ignorados := 0
	for _, e := range events {
		if e.Type != EventTypeHandleCreated || e.Producer.NHIID != handleNHI {
			continue
		}
		if parou {
			ignorados++
			continue
		}
		var p handleCriado
		if err := json.Unmarshal(e.Payload, &p); err != nil || p.Digest == "" {
			parou, ignorados = true, ignorados+1
			continue
		}
		ordinal, ok := agentruntime.HandleTokenOrdinal(p.Token)
		if !ok || ordinal != len(out)+1 {
			parou, ignorados = true, ignorados+1
			continue
		}
		out = append(out, p)
	}
	return out, ignorados
}

// handleStoreBanner é a linha de arranque do espaço. Sem espaço não sai linha nenhuma.
func handleStoreBanner(a *armazemDeHandles) []string {
	if a == nil {
		return nil
	}
	return []string{fmt.Sprintf("espaco de handles (AOS-550, ADR-043 §2.6): AOS_HANDLE_DIR=%q — o conteudo untrusted que viaja por handle vive AQUI, nao na via da %s: tectos PROPRIOS (%d bytes por handle, %d handles e %d bytes por run, %d bytes no volume), vocabulario ABERTO (binario incluido) e superficie de leitura PROPRIA — emissor %s, pelo que nada deste espaco aparece em GET /runs/{id}/artifacts nem e descarregavel com read:artifact, e nenhuma rota HTTP serve o conteudo (os METADADOS do evento — token, digest, passo e titular — saem na trajectoria do run, como os do artifact.created). Cifrado pelo cifrador por titular do no, com o run nos DADOS ASSOCIADOS (um blob copiado para a pasta de outro run nao abre), proveniencia selada em %s. Ocupado no arranque: %d bytes. RESIDUOS: o conteudo de um artefacto fica gravado DUAS vezes (aqui e no armazem de artefactos); nada apaga ficheiros deste espaco — so se DESFAZ uma selagem que nao chegou a valer (o evento que falha, e desde o AOS-556 a hospedagem que recusa a meio; um CRASH entre os selos e o registo de retoma deixa-os) —, pelo que o crypto-shred do titular os torna ilegiveis mas nao liberta bytes, e o tecto do volume cheio faz a %s recusar em TODOS os runs ate alguem apagar a pasta a mao",
		a.pasta, artifactWriteToolName, handleMaxBytes, handleMaxPorRun, handleMaxBytesPorRun, a.maximo, handleNHI, EventTypeHandleCreated, a.usado.Load(), artifactWriteToolName)}
}
