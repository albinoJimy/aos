package main

// REGISTO DE APAGAMENTOS (AOS-436) — a memória dos crypto-shreds que vive FORA da cadeia de
// backups.
//
// O DEFEITO QUE FECHA. O `deploy/server/backup.sh` copia, no MESMO bundle, o WORM (que afirma
// «a KEK do titular X foi destruída») e o `vault-data` (onde a KEK vive). Restaurar um bundle
// anterior a um apagamento repõe as DUAS coisas como estavam antes dele: a KEK volta, e com ela o
// conteúdo do titular volta a decifrar. A cadeia restaurada nem sequer sabe que o apagamento
// aconteceu — é anterior a ele. Nenhum mecanismo DENTRO do bundle pode cobrir este caso, porque é
// o bundle inteiro que recua no tempo.
//
// O QUE ESTE FICHEIRO É. Uma lista append-only de (nome da KEK, instante da destruição), escrita
// pelo nó a cada destruição CONFIRMADA pela custódia. O `backup.sh` copia-a EM CLARO para fora do
// bundle cifrado e a recolha leva-a para a máquina do operador; no restauro, o registo mais recente
// é importado ANTES de o nó arrancar, e a reconciliação ([reconciliadorDeApagamentos]) destrói de
// novo o que ele diz destruído.
//
// PORQUE PODE SAIR EM CLARO. Só leva nomes NÃO-REVERSÍVEIS — `aos-kek-<sha256(keyRef)>`, o mesmo
// nome que a chave Transit já tem no Vault — e o instante. Nunca o titular: a partir do registo não
// se chega a quem foi apagado, só se reconhece a chave dele quando ela reaparecer.
//
// MONOTÓNICO. O nó nunca reescreve nem remove linhas; só acrescenta. O registo mais recente é, por
// construção, um superconjunto de qualquer um anterior — e é isso que torna «importar o mais
// recente» a instrução certa no restauro. Linhas repetidas são inofensivas: a leitura fica com o
// instante MAIS RECENTE por nome.

import (
	"bufio"
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

// ErrRegistoDeApagamentos — o registo de apagamentos não pôde ser lido ou escrito. Fail-closed de
// prontidão: um apagamento confirmado que não chegou ao registo fica SEM a rede que o protege de um
// restauro de tudo antigo, e isso não pode passar por um nó saudável.
var ErrRegistoDeApagamentos = errors.New("aos: registo de apagamentos (AOS-436) ilegivel ou por escrever — um restauro de backup anterior pode ressuscitar KEKs destruidas sem que o no o detecte")

// cabecalhoRegistoApagamentos abre um registo novo. É comentário (a leitura ignora-o) e existe
// para quem abrir o ficheiro à mão saber o que tem à frente — é um ficheiro que um operador vai
// copiar entre máquinas no pior dia do ano.
const cabecalhoRegistoApagamentos = "# aos — registo de apagamentos (AOS-436), formato v1: <nome-da-KEK> <instante RFC3339 UTC>\n" +
	"# so nomes nao-reversiveis (aos-kek-<sha256 do keyRef>), nunca o titular; append-only e monotonico\n"

// reNomeApagamento é a forma EXACTA de um nome admissível. É também uma validação de SEGURANÇA: o
// nome vai parar a um caminho HTTP do Vault (`/v1/transit/keys/<nome>`), e o registo importado vem
// de fora do nó. Sem ela, uma linha forjada com `../` escolhia o caminho que o nó pede ao Vault.
var reNomeApagamento = regexp.MustCompile(`^aos-kek-[0-9a-f]{64}$`)

// nomeDeApagamento é o nome não-reversível sob o qual a KEK de um titular é registada. É o nome da
// chave Transit ([vaultKeyName]) por construção, e não por coincidência: o registo tem de nomear a
// chave exactamente como a custódia a conhece, sem nunca passar pelo titular.
func nomeDeApagamento(subjectID string) string {
	return vaultKeyName(audit.KeyRefFor(subjectID))
}

// entradaDeApagamento é uma linha do registo: que chave morreu, e quando.
type entradaDeApagamento struct {
	nome        string
	destruidaEm time.Time
}

func (e entradaDeApagamento) linha() string {
	return e.nome + " " + e.destruidaEm.UTC().Format(time.RFC3339) + "\n"
}

// registoDeApagamentos é o escritor do registo PRÓPRIO do nó. Seguro para concorrência: o shredder
// DSAR, o sink de expiração e a reconciliação podem escrever ao mesmo tempo.
type registoDeApagamentos struct {
	caminho string

	mu sync.Mutex
	// porEscrever são entradas cuja escrita FALHOU. Não se perdem: a próxima escrita (ou a
	// re-tentativa do laço de manutenção) escreve-as primeiro, e enquanto existirem a prontidão
	// da custódia fica vermelha ([vaultKeyVault.apagamentosFault]).
	porEscrever []entradaDeApagamento
}

func novoRegistoDeApagamentos(caminho string) *registoDeApagamentos {
	return &registoDeApagamentos{caminho: filepath.Clean(caminho)}
}

// acrescentar escreve as entradas no fim do ficheiro (criando-o com o cabeçalho se não existir),
// com fsync. As pendentes de uma falha anterior vão à frente. Em caso de erro NADA se perde: tudo
// fica em [porEscrever] até à próxima tentativa.
func (r *registoDeApagamentos) acrescentar(novas ...entradaDeApagamento) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	todas := append(append([]entradaDeApagamento(nil), r.porEscrever...), novas...)
	if len(todas) == 0 {
		return nil
	}
	if err := r.escrever(todas); err != nil {
		r.porEscrever = todas
		return fmt.Errorf("%w: %s: %v", ErrRegistoDeApagamentos, r.caminho, err)
	}
	r.porEscrever = nil
	return nil
}

// escrever faz o append físico. Chamado sob r.mu.
func (r *registoDeApagamentos) escrever(entradas []entradaDeApagamento) error {
	f, err := os.OpenFile(r.caminho, os.O_RDWR|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	st, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return err
	}
	var b strings.Builder
	switch {
	case st.Size() == 0:
		b.WriteString(cabecalhoRegistoApagamentos)
	default:
		// Uma escrita anterior cortada a meio (crash) deixa o ficheiro sem '\n' final. Fecha-se
		// essa linha antes de acrescentar, para a entrada nova não se colar ao fragmento — o
		// fragmento passa a ser uma linha malformada, que a leitura DENUNCIA em vez de engolir.
		ultimo := make([]byte, 1)
		if _, rerr := f.ReadAt(ultimo, st.Size()-1); rerr == nil && ultimo[0] != '\n' {
			b.WriteString("\n")
		}
	}
	for _, e := range entradas {
		b.WriteString(e.linha())
	}
	if _, err := f.WriteString(b.String()); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if st.Size() == 0 {
		// Ficheiro acabado de criar: a entrada de directório só é durável com fsync do pai
		// (molde de [LoadOrCreateIssuerKey]). Best-effort.
		if d, derr := os.Open(filepath.Dir(r.caminho)); derr == nil {
			_ = d.Sync()
			_ = d.Close()
		}
	}
	return nil
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

// lerRegistoDeApagamentos lê um registo e devolve, por nome, o instante de destruição MAIS RECENTE.
//
// ESTRITO, e é de propósito: uma linha que não é um nome admissível seguido de um instante RFC3339
// é ERRO, com o número da linha. A alternativa — saltá-la — seria perder em silêncio um apagamento
// que o registo existe para lembrar. A prontidão fica vermelha e o operador corrige o ficheiro, que
// é texto e não é segredo.
//
// ausenteOK distingue os dois chamadores: o registo PRÓPRIO pode ainda não existir (nó que nunca
// apagou nada); um registo IMPORTADO que não existe é um restauro que não importou o que devia.
func lerRegistoDeApagamentos(caminho string, ausenteOK bool) (map[string]time.Time, error) {
	out := make(map[string]time.Time)
	f, err := os.Open(filepath.Clean(caminho))
	if err != nil {
		if ausenteOK && errors.Is(err, os.ErrNotExist) {
			return out, nil
		}
		return nil, fmt.Errorf("%w: %s: %v", ErrRegistoDeApagamentos, caminho, err)
	}
	defer f.Close()
	rd := bufio.NewReader(f)
	for n := 1; ; n++ {
		linha, rerr := rd.ReadString('\n')
		if rerr != nil && !errors.Is(rerr, io.EOF) {
			return nil, fmt.Errorf("%w: %s: %v", ErrRegistoDeApagamentos, caminho, rerr)
		}
		t := strings.TrimSpace(linha)
		if t != "" && !strings.HasPrefix(t, "#") {
			campos := strings.Fields(t)
			if len(campos) != 2 || !reNomeApagamento.MatchString(campos[0]) {
				return nil, fmt.Errorf("%w: %s:%d: linha malformada (esperado `aos-kek-<64 hex> <RFC3339>`)", ErrRegistoDeApagamentos, caminho, n)
			}
			quando, perr := time.Parse(time.RFC3339, campos[1])
			if perr != nil {
				return nil, fmt.Errorf("%w: %s:%d: instante ilegivel: %v", ErrRegistoDeApagamentos, caminho, n, perr)
			}
			if quando.After(out[campos[0]]) {
				out[campos[0]] = quando.UTC()
			}
		}
		if errors.Is(rerr, io.EOF) {
			return out, nil
		}
	}
}

// nomesOrdenados devolve as chaves de um mapa por ordem — a reconciliação percorre-as assim para
// que duas passagens sobre o mesmo estado façam exactamente os mesmos pedidos pela mesma ordem.
func nomesOrdenados[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
