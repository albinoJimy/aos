package main

// REGISTO DE APAGAMENTOS (AOS-436) — a memória dos crypto-shreds que vive FORA do bundle.
//
// O QUE É. Uma lista append-only de destruições de KEK CONFIRMADAS pela custódia, escrita pelo nó.
// O `deploy/server/backup.sh` copia-a em claro para fora do bundle cifrado, a recolha leva-a para a
// máquina do operador, e num restauro de um bundle ANTERIOR ao último o registo mais recente é
// importado antes de o nó arrancar — a reconciliação ([reconciliadorDeApagamentos]) destrói de novo
// o que ele diz destruído.
//
// A FORMA (v3), com UMA chave secreta do nó (`<registo>.chave`, 32 bytes, no volume de dados — viaja
// SÓ dentro do bundle cifrado, nunca ao lado do registo):
//
//	<id> <instante RFC3339 UTC> <mac>
//	id  = HMAC-SHA256(k, "aos436/id\n"  ‖ nome-da-KEK-no-Vault)
//	mac = HMAC-SHA256(k, "aos436/mac\n" ‖ mac-da-linha-anterior ‖ "\n" ‖ id ‖ " " ‖ instante)
//
// O que cada peça fecha, e foi medido por duas revisões adversariais:
//
//   - sem a chave o `id` não se inverte — o nome do Vault, `aos-kek-<sha256>` de um keyRef público,
//     é invertível por dicionário de utilizadores; o HMAC não;
//   - sem a chave uma linha não se forja, e uma linha legítima não se re-data (o instante está sob
//     o MAC);
//   - o MAC ENCADEADO faz de uma linha removida, inserida ou trocada a meio uma quebra visível: a
//     linha seguinte deixa de autenticar. O que o encadeamento NÃO vê é o corte do FIM do ficheiro
//     — um registo truncado é indistinguível de um registo mais antigo. Isso fica declarado e é
//     vigiado fora daqui (a reconciliação recusa um importado que não contenha o que o bundle
//     restaurado já sabe; a recolha recusa um registo que perdeu entradas).
//
// A CHAVE NUNCA SE RECRIA POR CIMA DE UM REGISTO. Um registo com entradas e sem chave, ou um
// restauro que pede uma importação sem a chave presente, é FALHA — criar outra chave em silêncio
// deixava o registo ilegível para sempre e, pior, fazia o nó escrever linhas sob a chave errada no
// mesmo ficheiro. A chave só nasce quando não há registo nenhum, e nasce atómica (temporário +
// link): um crash a meio deixa um temporário a mais, nunca uma chave curta.
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
// rejeitadas. Fail-closed: um apagamento que não chega ao registo fica sem a rede que o protege de
// um restauro, e uma linha rejeitada é alguém — ou um relógio — a pedir uma destruição que o nó
// recusou fazer.
var ErrRegistoDeApagamentos = errors.New("aos: registo de apagamentos (AOS-436) ilegivel, por escrever ou com linhas rejeitadas")

const (
	// cabecalhoRegistoApagamentos abre um registo novo. É comentário (a leitura ignora-o).
	cabecalhoRegistoApagamentos = "# aos — registo de apagamentos (AOS-436), formato v3: <id> <instante RFC3339 UTC> <mac encadeado>\n" +
		"# id e mac sao HMAC-SHA256 sob a chave do no (fica no volume de dados, so dentro do bundle cifrado);\n" +
		"# sem ela o id nao identifica o titular e uma linha nao se forja nem se remove a meio. Append-only.\n"

	// sufixoDaChaveDoRegisto nomeia o ficheiro da chave, ao lado do registo próprio.
	sufixoDaChaveDoRegisto = ".chave"

	// folgaDoFuturo é o que um instante registado pode estar À FRENTE do relógio do nó. Um apagamento
	// datado para lá disto é um relógio errado ou uma linha fabricada — aceite, condenaria TODAS as
	// gerações futuras da chave a cada arranque.
	folgaDoFuturo = 5 * time.Minute
)

// reHex64 é a forma exacta de um id ou de um mac: 64 hexadecimais MINÚSCULOS.
var reHex64 = regexp.MustCompile(`^[0-9a-f]{64}$`)

// reNomeApagamento é a forma exacta do nome de uma KEK no Vault. Vai parar a um caminho HTTP
// (`/v1/transit/keys/<nome>`): só esta forma passa.
var reNomeApagamento = regexp.MustCompile(`^aos-kek-[0-9a-f]{64}$`)

// nomeDaKEK é o nome da chave Transit de um titular — o que o Vault conhece. NÃO entra no registo
// nem em erros e logs (é invertível por dicionário); entra o seu HMAC.
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
// concorrência dentro do processo. Entre processos não há lock: o volume é de UM nó.
type registoDeApagamentos struct {
	caminho string
	// podeCriarChave é falso quando há uma importação pedida: um restauro que importa tem de trazer
	// a chave do bundle — uma nova nunca autenticaria o importado.
	podeCriarChave bool

	mu sync.Mutex
	// chave é carregada (ou criada) na primeira utilização. nil ⇒ ainda não, ou falhou — e nesse
	// caso nada se lê nem se escreve, tudo fica pendente.
	chave []byte
	// porEscrever são entradas cuja escrita FALHOU. A próxima escrita escreve-as primeiro, e
	// enquanto existirem a prontidão da custódia fica vermelha.
	porEscrever []entradaDeApagamento
	// avisos acumula o que a escrita teve de corrigir (um fragmento final truncado), para o log.
	avisos []string
}

func novoRegistoDeApagamentos(caminho string, podeCriarChave bool) *registoDeApagamentos {
	return &registoDeApagamentos{caminho: filepath.Clean(caminho), podeCriarChave: podeCriarChave}
}

// prepararChave carrega a chave, ou cria-a — só quando é seguro. Chamado sob r.mu.
func (r *registoDeApagamentos) prepararChave() error {
	if r.chave != nil {
		return nil
	}
	caminhoChave := r.caminho + sufixoDaChaveDoRegisto
	raw, err := os.ReadFile(caminhoChave)
	if err == nil {
		if len(raw) != 32 {
			return fmt.Errorf("%w: %s tem %d bytes (esperados 32) — reponha-a do bundle mais recente; NAO se cria outra por cima de um registo", ErrRegistoDeApagamentos, caminhoChave, len(raw))
		}
		r.chave = raw
		return nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("%w: ler %s: %v", ErrRegistoDeApagamentos, caminhoChave, err)
	}
	if !r.podeCriarChave {
		return fmt.Errorf("%w: a chave %s NAO existe e ha uma importacao pedida — um restauro tem de trazer a chave do bundle mais recente (uma chave nova nunca autenticaria o importado); nada foi criado nem escrito", ErrRegistoDeApagamentos, caminhoChave)
	}
	temEntradas, verr := registoTemEntradas(r.caminho)
	if verr != nil {
		return verr
	}
	if temEntradas {
		return fmt.Errorf("%w: o registo %s tem entradas e a chave %s NAO existe — reponha-a do bundle mais recente; criar outra deixava o registo ilegivel para sempre (ver o runbook se ela se perdeu)", ErrRegistoDeApagamentos, r.caminho, caminhoChave)
	}
	chave, err := criarChaveAtomica(caminhoChave)
	if err != nil {
		return err
	}
	r.chave = chave
	return nil
}

// registoTemEntradas diz se o ficheiro tem alguma linha que não seja comentário nem vazia.
func registoTemEntradas(caminho string) (bool, error) {
	raw, err := os.ReadFile(caminho)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("%w: ler %s: %v", ErrRegistoDeApagamentos, caminho, err)
	}
	for _, l := range strings.Split(string(raw), "\n") {
		if t := strings.TrimSpace(l); t != "" && !strings.HasPrefix(t, "#") {
			return true, nil
		}
	}
	return false, nil
}

// criarChaveAtomica escreve 32 bytes num temporário, faz fsync e publica-o com `link` — que falha
// se o destino já existir, pelo que dois arranques concorrentes não se pisam e um crash a meio deixa
// um temporário órfão, nunca uma chave curta.
func criarChaveAtomica(caminho string) ([]byte, error) {
	nova := make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, nova); err != nil {
		return nil, fmt.Errorf("%w: gerar a chave: %v", ErrRegistoDeApagamentos, err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(caminho), filepath.Base(caminho)+".tmp-*")
	if err != nil {
		return nil, fmt.Errorf("%w: criar temporario da chave: %v", ErrRegistoDeApagamentos, err)
	}
	nomeTmp := tmp.Name()
	defer os.Remove(nomeTmp)
	if err := tmp.Chmod(0o600); err != nil && !errors.Is(err, os.ErrInvalid) {
		_ = tmp.Close()
		return nil, fmt.Errorf("%w: permissoes do temporario: %v", ErrRegistoDeApagamentos, err)
	}
	if _, err := tmp.Write(nova); err != nil {
		_ = tmp.Close()
		return nil, fmt.Errorf("%w: gravar a chave: %v", ErrRegistoDeApagamentos, err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return nil, fmt.Errorf("%w: fsync da chave: %v", ErrRegistoDeApagamentos, err)
	}
	if err := tmp.Close(); err != nil {
		return nil, err
	}
	if err := os.Link(nomeTmp, caminho); err != nil {
		if errors.Is(err, os.ErrExist) {
			raw, rerr := os.ReadFile(caminho)
			if rerr == nil && len(raw) == 32 {
				return raw, nil // outro arranque ganhou: fica a dele
			}
		}
		return nil, fmt.Errorf("%w: publicar a chave %s: %v", ErrRegistoDeApagamentos, caminho, err)
	}
	if d, derr := os.Open(filepath.Dir(caminho)); derr == nil {
		_ = d.Sync()
		_ = d.Close()
	}
	return nova, nil
}

func (r *registoDeApagamentos) hmacHex(dominio string, partes ...string) string {
	m := hmac.New(sha256.New, r.chave)
	m.Write([]byte(dominio))
	for _, p := range partes {
		m.Write([]byte(p))
	}
	return hex.EncodeToString(m.Sum(nil))
}

// idDe devolve o id de uma KEK pelo seu nome no Vault.
func (r *registoDeApagamentos) idDe(nome string) string { return r.hmacHex("aos436/id\n", nome) }

// macDe é o MAC ENCADEADO de uma linha: cobre a linha anterior, o id e o instante.
func (r *registoDeApagamentos) macDe(anterior, id, instante string) string {
	return r.hmacHex("aos436/mac\n", anterior, "\n", id, " ", instante)
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

// acrescentar escreve as entradas no fim do ficheiro (criando-o com o cabeçalho), com fsync. As
// pendentes de uma falha anterior vão à frente. Em caso de erro nada se perde.
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

// escrever faz o append físico, encadeando cada linha na última que o ficheiro tem. Chamado sob r.mu.
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
	anterior := ""
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
		anterior, err = ultimoMac(f, tam)
		if err != nil {
			return err
		}
	}
	var b strings.Builder
	if tam == 0 {
		b.WriteString(cabecalhoRegistoApagamentos)
	}
	for _, e := range entradas {
		id := e.id
		if id == "" {
			id = r.idDe(e.nome)
		}
		t := e.destruidaEm.UTC().Format(time.RFC3339)
		mac := r.macDe(anterior, id, t)
		b.WriteString(id + " " + t + " " + mac + "\n")
		anterior = mac
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

// ultimoMac devolve o MAC da última linha de registo do ficheiro (até `tam`, que termina em '\n'),
// ou "" se não houver nenhuma. É o elo onde a próxima linha se encadeia.
func ultimoMac(f *os.File, tam int64) (string, error) {
	raw := make([]byte, tam)
	if _, err := f.ReadAt(raw, 0); err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	linhas := strings.Split(string(raw), "\n")
	for i := len(linhas) - 1; i >= 0; i-- {
		t := strings.TrimSpace(linhas[i])
		if t == "" || strings.HasPrefix(t, "#") {
			continue
		}
		campos := strings.Fields(t)
		return campos[len(campos)-1], nil
	}
	return "", nil
}

// inicioDoFragmentoFinal devolve o offset a seguir ao último '\n' (ou 0).
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
	// validas: id → instante MAIS RECENTE, só de linhas que autenticam na cadeia e têm instante são.
	validas map[string]time.Time
	// rejeitadas: uma descrição por linha recusada (ficheiro, número, motivo) — nunca a linha.
	rejeitadas []string
	// fragmento: havia um fragmento final sem '\n', ignorado (escrita interrompida).
	fragmento bool
}

// ler lê um registo sob a chave deste registo. `agora` é o relógio do nó, para o limite do futuro.
//
// O ENCADEAMENTO verifica-se contra o MAC ESCRITO na linha anterior, autêntico ou não: assim cada
// linha autentica-se sozinha no seu lugar, e uma remoção, inserção ou troca a meio custa a linha
// seguinte — que fica rejeitada e nomeada.
//
// POR LINHA, e não tudo-ou-nada: uma linha rejeitada não apaga as válidas que a rodeiam. Mas uma
// rejeição deixa a fonte POR PROVAR, e NUNCA destrói nada.
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
	anterior := ""
	for n := 1; sc.Scan(); n++ {
		t := strings.TrimSpace(sc.Text())
		if t == "" || strings.HasPrefix(t, "#") {
			continue
		}
		campos := strings.Fields(t)
		if len(campos) != 3 || !reHex64.MatchString(campos[0]) || !reHex64.MatchString(campos[2]) {
			out.rejeitadas = append(out.rejeitadas, fmt.Sprintf("%s:%d: linha malformada (esperado `<id 64 hex> <RFC3339> <mac 64 hex>`)", caminho, n))
			if len(campos) > 0 {
				anterior = campos[len(campos)-1]
			}
			continue
		}
		esperado := r.macDe(anterior, campos[0], campos[1])
		anterior = campos[2]
		if !hmac.Equal([]byte(campos[2]), []byte(esperado)) {
			out.rejeitadas = append(out.rejeitadas, fmt.Sprintf("%s:%d: MAC invalido — linha forjada, re-datada, ou a cadeia partiu-se antes dela (linha removida/inserida/trocada) ou chave diferente; NAO destroi nada", caminho, n))
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
