package main

// REGISTO DE APAGAMENTOS (AOS-436) — a memória dos crypto-shreds que vive FORA do bundle.
//
// O QUE É. Uma lista append-only de destruições de KEK CONFIRMADAS pela custódia, escrita pelo nó.
// O `deploy/server/backup.sh` copia-a em claro para fora do bundle cifrado, a recolha leva-a para a
// máquina do operador, e num restauro de um bundle ANTERIOR a um apagamento o registo mais recente
// é importado antes de o nó arrancar — a reconciliação ([reconciliadorDeApagamentos]) destrói de
// novo o que ele diz destruído.
//
// O QUE A REVISÃO ADVERSARIAL DO PRIMEIRO DESENHO MEDIU, e que esta forma fecha:
//
//	(1) um registo IMPORTADO é entrada vinda de fora do nó, e uma linha forjada destruía a KEK
//	    VIVA de qualquer titular — irreversivelmente. Sem integridade, «importar o registo» era
//	    «dar a quem escreve o ficheiro o poder de apagar quem quiser»;
//	(2) o nome `aos-kek-<sha256(keyRef)>` NÃO é irreversível: o keyRef é `aos.audit.pii:` + um
//	    identificador de utilizador, sem sal, e um dicionário de utilizadores inverte-o. O ficheiro
//	    em claro no portátil dizia quem exerceu o Art. 17, e quando.
//
// A FORMA (v2), com UMA chave secreta do nó (`<registo>.chave`, 32 bytes, no volume de dados — viaja
// SÓ dentro do bundle cifrado, nunca ao lado do registo):
//
//	<id> <instante RFC3339 UTC> <mac>
//	id  = HMAC-SHA256(k, "aos436/id\n"  ‖ nome-da-KEK-no-Vault)
//	mac = HMAC-SHA256(k, "aos436/mac\n" ‖ id ‖ " " ‖ instante)
//
// Sem a chave, o `id` não se inverte (é um PRF, não um hash público) e uma linha não se forja (o MAC
// não bate). Uma linha rejeitada é NOMEADA e NUNCA destrói. A reconciliação volta do `id` ao nome
// enumerando as chaves que o Vault TEM — que são as únicas que interessam.
//
// LINHA CORTADA. Só o fragmento FINAL sem '\n' é uma escrita interrompida: a leitura ignora-o (e
// di-lo), e a próxima escrita trunca-o antes de acrescentar. Uma linha COMPLETA malformada continua
// a ser rejeitada — não há como distinguir corrupção de adulteração, e nenhuma das duas destrói.

import (
	"bufio"
	"bytes"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	audit "github.com/aos-ref/platform/audit"
)

// ErrRegistoDeApagamentos — o registo de apagamentos não pôde ser lido ou escrito, ou tem linhas
// rejeitadas. Fail-closed de prontidão: um apagamento que não chega ao registo fica sem a rede que o
// protege de um restauro, e uma linha rejeitada é alguém — ou um relógio — a pedir uma destruição
// que o nó recusou fazer. As duas coisas têm de se ver.
var ErrRegistoDeApagamentos = errors.New("aos: registo de apagamentos (AOS-436) ilegivel, por escrever ou com linhas rejeitadas")

const (
	// cabecalhoRegistoApagamentos abre um registo novo. É comentário (a leitura ignora-o).
	cabecalhoRegistoApagamentos = "# aos — registo de apagamentos (AOS-436), formato v2: <id> <instante RFC3339 UTC> <mac>\n" +
		"# id e mac sao HMAC-SHA256 sob a chave do no (fica no volume de dados, so dentro do bundle cifrado);\n" +
		"# sem ela o id nao identifica o titular e uma linha nao se forja. Append-only e monotonico.\n"

	// sufixoDaChaveDoRegisto nomeia o ficheiro da chave, ao lado do registo próprio.
	sufixoDaChaveDoRegisto = ".chave"

	// folgaDoFuturo é o que um instante registado pode estar À FRENTE do relógio do nó. Um apagamento
	// datado para lá disto não é um apagamento: é um relógio errado ou uma linha fabricada — e as
	// duas coisas, aceites, deixavam um instante no futuro a condenar TODAS as gerações futuras da
	// chave, a cada arranque. Cinco minutos cobrem o desvio normal entre nós sem abrir essa porta.
	folgaDoFuturo = 5 * time.Minute
)

// reHex64 é a forma exacta de um id ou de um mac: 64 hexadecimais MINÚSCULOS.
var reHex64 = regexp.MustCompile(`^[0-9a-f]{64}$`)

// reNomeApagamento é a forma exacta do nome de uma KEK no Vault. Vai parar a um caminho HTTP
// (`/v1/transit/keys/<nome>`): só esta forma passa.
var reNomeApagamento = regexp.MustCompile(`^aos-kek-[0-9a-f]{64}$`)

// nomeDaKEK é o nome da chave Transit de um titular — o que o Vault conhece. NÃO entra no registo
// (é invertível por dicionário); entra o seu HMAC.
func nomeDaKEK(subjectID string) string {
	return vaultKeyName(audit.KeyRefFor(subjectID))
}

// entradaDeApagamento é uma linha do registo: que chave, e quando morreu. A chave vem pelo `id`
// (entradas importadas, que nunca tiveram o nome) ou pelo `nome` do Vault (destruições feitas por
// este nó); o `id` do nome só se calcula na escrita, porque a chave do registo pode ainda não estar
// legível quando a destruição acontece — e a entrada não se pode perder por isso.
type entradaDeApagamento struct {
	id          string
	nome        string
	destruidaEm time.Time
}

// registoDeApagamentos é o registo PRÓPRIO do nó e a chave que o autentica. Seguro para
// concorrência: o shredder DSAR, o sink de expiração e a reconciliação escrevem ao mesmo tempo.
type registoDeApagamentos struct {
	caminho string

	mu sync.Mutex
	// chave é carregada (ou criada) na primeira utilização. nil ⇒ ainda não, ou falhou — e nesse
	// caso nada se lê nem se escreve, tudo fica pendente e a prontidão vermelha.
	chave []byte
	// chaveNova diz que a chave foi CRIADA por este processo. Num restauro isso significa que o
	// bundle é anterior a ela, e que um registo importado escrito com a antiga não vai autenticar.
	chaveNova bool
	// porEscrever são entradas cuja escrita FALHOU. Não se perdem: a próxima escrita escreve-as
	// primeiro, e enquanto existirem a prontidão da custódia fica vermelha.
	porEscrever []entradaDeApagamento
	// avisos acumula o que a escrita teve de corrigir (um fragmento final truncado) para o
	// chamador o pôr no log — o registo não tem log próprio.
	avisos []string
}

func novoRegistoDeApagamentos(caminho string) *registoDeApagamentos {
	return &registoDeApagamentos{caminho: filepath.Clean(caminho)}
}

// prepararChave carrega (ou cria, UMA vez) a chave do registo. Idempotente; chamado sob r.mu.
func (r *registoDeApagamentos) prepararChave() error {
	if r.chave != nil {
		return nil
	}
	chave, nova, err := carregarOuCriarChaveDoRegisto(r.caminho + sufixoDaChaveDoRegisto)
	if err != nil {
		return err
	}
	r.chave, r.chaveNova = chave, nova
	return nil
}

// idsDe devolve o id de cada nome, com a chave carregada. Erro ⇒ a chave não está legível.
func (r *registoDeApagamentos) idsDe(nomes []string) (map[string]string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.prepararChave(); err != nil {
		return nil, err
	}
	out := make(map[string]string, len(nomes))
	for _, n := range nomes {
		out[r.idDe(n)] = n
	}
	return out, nil
}

// foiCriadaAgora diz se a chave foi criada por este processo.
func (r *registoDeApagamentos) foiCriadaAgora() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.chaveNova
}

// carregarOuCriarChaveDoRegisto — molde de [LoadOrCreateIssuerKey]: lê a chave se existir; senão
// gera 32 bytes por CSPRNG, grava-os 0600 com O_EXCL e fsync (ficheiro e directório).
func carregarOuCriarChaveDoRegisto(caminho string) ([]byte, bool, error) {
	raw, err := os.ReadFile(caminho)
	if err == nil {
		if len(raw) != 32 {
			return nil, false, fmt.Errorf("%w: %s nao tem 32 bytes", ErrRegistoDeApagamentos, caminho)
		}
		return raw, false, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, false, fmt.Errorf("%w: ler %s: %v", ErrRegistoDeApagamentos, caminho, err)
	}
	nova := make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, nova); err != nil {
		return nil, false, fmt.Errorf("%w: gerar a chave: %v", ErrRegistoDeApagamentos, err)
	}
	f, err := os.OpenFile(caminho, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return carregarOuCriarChaveDoRegisto(caminho)
		}
		return nil, false, fmt.Errorf("%w: criar %s: %v", ErrRegistoDeApagamentos, caminho, err)
	}
	if _, err := f.Write(nova); err != nil {
		_ = f.Close()
		return nil, false, fmt.Errorf("%w: gravar %s: %v", ErrRegistoDeApagamentos, caminho, err)
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return nil, false, fmt.Errorf("%w: fsync %s: %v", ErrRegistoDeApagamentos, caminho, err)
	}
	if err := f.Close(); err != nil {
		return nil, false, err
	}
	if d, derr := os.Open(filepath.Dir(caminho)); derr == nil {
		_ = d.Sync()
		_ = d.Close()
	}
	return nova, true, nil
}

func (r *registoDeApagamentos) hmacHex(dominio, dados string) string {
	m := hmac.New(sha256.New, r.chave)
	m.Write([]byte(dominio))
	m.Write([]byte(dados))
	return hex.EncodeToString(m.Sum(nil))
}

// idDe devolve o id de uma KEK pelo seu nome no Vault.
func (r *registoDeApagamentos) idDe(nome string) string { return r.hmacHex("aos436/id\n", nome) }

func (r *registoDeApagamentos) macDe(id, instante string) string {
	return r.hmacHex("aos436/mac\n", id+" "+instante)
}

func (r *registoDeApagamentos) linha(e entradaDeApagamento) string {
	id := e.id
	if id == "" {
		id = r.idDe(e.nome)
	}
	t := e.destruidaEm.UTC().Format(time.RFC3339)
	return id + " " + t + " " + r.macDe(id, t) + "\n"
}

// acrescentar escreve as entradas no fim do ficheiro (criando-o com o cabeçalho), com fsync. As
// pendentes de uma falha anterior vão à frente. Em caso de erro nada se perde: tudo fica em
// [porEscrever] até à próxima tentativa.
func (r *registoDeApagamentos) acrescentar(novas ...entradaDeApagamento) error {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	todas := append(append([]entradaDeApagamento(nil), r.porEscrever...), novas...)
	if len(todas) == 0 {
		return nil
	}
	if err := r.prepararChave(); err != nil {
		r.porEscrever = todas
		return err
	}
	if err := r.escrever(todas); err != nil {
		r.porEscrever = todas
		return fmt.Errorf("%w: %s: %v", ErrRegistoDeApagamentos, r.caminho, err)
	}
	r.porEscrever = nil
	return nil
}

// escrever faz o append físico. Chamado sob r.mu.
//
// FRAGMENTO FINAL. Uma escrita anterior interrompida (crash, disco cheio a meio) deixa o ficheiro
// sem '\n' final. Esse fragmento é truncado ANTES de acrescentar — é a única escrita não-append que
// o registo conhece, e só alcança bytes que nunca chegaram a ser uma linha. Sem isto, a re-tentativa
// colava a linha nova ao fragmento e fixava uma linha malformada para sempre.
func (r *registoDeApagamentos) escrever(entradas []entradaDeApagamento) error {
	f, err := os.OpenFile(r.caminho, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return err
	}
	tam := st.Size()
	if tam > 0 {
		corte, terr := inicioDoFragmentoFinal(f, tam)
		if terr != nil {
			return terr
		}
		if corte < tam {
			if err := f.Truncate(corte); err != nil {
				return err
			}
			r.avisos = append(r.avisos, fmt.Sprintf("%s: fragmento final de %d byte(s) sem fim de linha (escrita interrompida) TRUNCADO antes de acrescentar", r.caminho, tam-corte))
			tam = corte
		}
	}
	var b strings.Builder
	if tam == 0 {
		b.WriteString(cabecalhoRegistoApagamentos)
	}
	for _, e := range entradas {
		b.WriteString(r.linha(e))
	}
	if _, err := f.WriteAt([]byte(b.String()), tam); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}
	if st.Size() == 0 {
		if d, derr := os.Open(filepath.Dir(r.caminho)); derr == nil {
			_ = d.Sync()
			_ = d.Close()
		}
	}
	return nil
}

// inicioDoFragmentoFinal devolve o offset a seguir ao último '\n' (ou 0). Lê de trás para a frente
// em blocos: o registo cresce sem limite e não se carrega inteiro para acrescentar uma linha.
func inicioDoFragmentoFinal(f *os.File, tam int64) (int64, error) {
	const bloco = 4096
	buf := make([]byte, bloco)
	for fim := tam; fim > 0; {
		ini := fim - bloco
		if ini < 0 {
			ini = 0
		}
		n, err := f.ReadAt(buf[:fim-ini], ini)
		if err != nil && !errors.Is(err, io.EOF) {
			return 0, err
		}
		if i := bytes.LastIndexByte(buf[:n], '\n'); i >= 0 {
			return ini + int64(i) + 1, nil
		}
		fim = ini
	}
	return 0, nil
}

// pendentes devolve quantas entradas continuam por escrever.
func (r *registoDeApagamentos) pendentes() int {
	if r == nil {
		return 0
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.porEscrever)
}

// tirarAvisos devolve (e esquece) o que a escrita teve de corrigir.
func (r *registoDeApagamentos) tirarAvisos() []string {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	a := r.avisos
	r.avisos = nil
	return a
}

// descarregar tenta escrever as entradas pendentes de uma falha anterior.
func (r *registoDeApagamentos) descarregar() error { return r.acrescentar() }

// leituraDoRegisto é o que uma leitura aproveitou e o que recusou.
type leituraDoRegisto struct {
	// validas: id → instante MAIS RECENTE, só de linhas com MAC válido e instante são.
	validas map[string]time.Time
	// rejeitadas: uma descrição por linha recusada (número, motivo) — nunca o conteúdo da linha.
	rejeitadas []string
	// fragmento: havia um fragmento final sem '\n', ignorado (escrita interrompida).
	fragmento bool
}

// ler lê um registo sob a chave deste registo. `agora` é o relógio do nó, para o limite do futuro.
//
// POR LINHA, e não tudo-ou-nada: uma linha rejeitada não apaga as válidas que a rodeiam — a
// independência que a revisão pediu vale também dentro de uma fonte. Mas uma rejeição deixa a
// fonte POR PROVAR (o chamador torna-o prontidão vermelha), e NUNCA destrói nada.
//
// ausenteOK distingue o registo próprio (pode não existir ainda) de um importado (foi pedido).
func (r *registoDeApagamentos) ler(caminho string, ausenteOK bool, agora time.Time) (leituraDoRegisto, error) {
	out := leituraDoRegisto{validas: make(map[string]time.Time)}
	r.mu.Lock()
	kerr := r.prepararChave()
	r.mu.Unlock()
	if kerr != nil {
		return out, kerr
	}
	raw, err := os.ReadFile(filepath.Clean(caminho))
	if err != nil {
		if ausenteOK && errors.Is(err, os.ErrNotExist) {
			return out, nil
		}
		return out, fmt.Errorf("%w: %s: %v", ErrRegistoDeApagamentos, caminho, err)
	}
	if n := len(raw); n > 0 && raw[n-1] != '\n' {
		out.fragmento = true
		raw = raw[:bytes.LastIndexByte(raw, '\n')+1]
	}
	sc := bufio.NewScanner(bytes.NewReader(raw))
	sc.Buffer(make([]byte, 0, 4096), 1<<20)
	for n := 1; sc.Scan(); n++ {
		t := strings.TrimSpace(sc.Text())
		if t == "" || strings.HasPrefix(t, "#") {
			continue
		}
		campos := strings.Fields(t)
		if len(campos) != 3 || !reHex64.MatchString(campos[0]) || !reHex64.MatchString(campos[2]) {
			out.rejeitadas = append(out.rejeitadas, fmt.Sprintf("%s:%d: linha malformada (esperado `<id 64 hex> <RFC3339> <mac 64 hex>`)", caminho, n))
			continue
		}
		if !hmac.Equal([]byte(campos[2]), []byte(r.macDe(campos[0], campos[1]))) {
			out.rejeitadas = append(out.rejeitadas, fmt.Sprintf("%s:%d: MAC invalido — linha nao escrita por este no (ou chave diferente); NAO destroi nada", caminho, n))
			continue
		}
		quando, perr := time.Parse(time.RFC3339, campos[1])
		if perr != nil {
			out.rejeitadas = append(out.rejeitadas, fmt.Sprintf("%s:%d: instante ilegivel", caminho, n))
			continue
		}
		if quando.After(agora.Add(folgaDoFuturo)) {
			out.rejeitadas = append(out.rejeitadas, fmt.Sprintf("%s:%d: instante %s no FUTURO (relogio errado?) — rejeitado, NAO destroi nada", caminho, n, campos[1]))
			continue
		}
		if quando.After(out.validas[campos[0]]) {
			out.validas[campos[0]] = quando.UTC()
		}
	}
	if err := sc.Err(); err != nil {
		return out, fmt.Errorf("%w: %s: %v", ErrRegistoDeApagamentos, caminho, err)
	}
	return out, nil
}

// nomesOrdenados devolve as chaves de um mapa por ordem — duas passagens sobre o mesmo estado fazem
// os mesmos pedidos pela mesma ordem.
func nomesOrdenados[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
