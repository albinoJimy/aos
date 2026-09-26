package main

// metricas_do_consumo.go — O QUE A DRENAGEM FEZ, NUM FICHEIRO QUE O SENSOR LÊ (AOS-443).
//
// # PORQUE UM FICHEIRO, E NÃO UM `/metrics`
//
// O `consume` drena uma vez e termina (ver consumir.go): não há processo vivo para responder a um
// scrape. Um endpoint HTTP obrigaria a transformar o `aos-orq` num serviço de longa duração — com
// healthcheck, reinício e superfície de rede próprios —, que é exactamente o que o AOS-423 e o
// ADR-031 §3(b) recusaram. A forma que serve um processo curto é a do *textfile collector* do
// Prometheus: no fim de cada drenagem escreve-se, de forma ATÓMICA, um ficheiro em formato de
// texto de exposição, que qualquer leitor lê sem falar com o processo.
//
// # OS CONTADORES ACUMULAM-SE AQUI, E NÃO NO LEITOR
//
// Cada drenagem lê o ficheiro anterior, soma o que fez, e reescreve-o. Assim o ficheiro é sempre a
// história inteira (contadores monotónicos, como o Prometheus os espera) e o leitor — hoje o
// `alerta-nhi.sh`, em bash — não tem de fazer contas. Um ficheiro anterior ilegível recomeça do
// zero, e di-lo no stderr: um contador que volta a zero é um RESET, que o Prometheus sabe ler;
// inventar valores não seria.
//
// UM ESCRITOR DE CADA VEZ é garantia de quem invoca (o `flock` do drenar-planos.sh e o oneshot do
// systemd), não deste código. Duas drenagens em simultâneo sobre o mesmo ficheiro perdem
// incrementos — contam A MENOS, nunca a mais. Está declarado no AOS-443.
//
// # O QUE O FICHEIRO NÃO LEVA
//
// Nenhum identificador: nem `run_id`, nem objectivo, nem nós. Só contagens, classes, códigos e
// durações — o ficheiro sai do volume do `aos-orq` para uma pasta do utilizador `aos` e não é
// alcançado pelo apagamento DSAR. O resumo POR PEDIDO, com o `run_id`, vai para o nó (no `detail`
// do desfecho — ver [resumoDoPedido]), que é quem o guarda sob a governação dele.

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/aos-ref/control-plane/orchestrator/plan"
)

// nomeDoFicheiroDeMetricas é o ficheiro por omissão, ao lado do WAL do `consume`.
const nomeDoFicheiroDeMetricas = "aos-orq-consume.prom"

// Nomes das séries. O prefixo `aos_orq_consume_` diz de onde vêm; o sufixo segue as convenções do
// Prometheus (`_total` para contadores, unidade no nome).
const (
	metricaDrenagens          = "aos_orq_consume_drenagens_total"
	metricaReclamados         = "aos_orq_consume_pedidos_reclamados_total"
	metricaRetomas            = "aos_orq_consume_retomas_total"
	metricaOrigem             = "aos_orq_consume_origem_total"
	metricaDesfechos          = "aos_orq_consume_desfechos_total"
	metricaNaoReportados      = "aos_orq_consume_desfechos_nao_reportados_total"
	metricaDuracao            = "aos_orq_consume_plano_duracao_segundos"
	metricaFalhasConsecutivas = "aos_orq_consume_falhas_consecutivas"
	metricaUltimaDrenagem     = "aos_orq_consume_ultima_drenagem_timestamp_seconds"
	metricaUltimaPedidos      = "aos_orq_consume_ultima_drenagem_pedidos"
)

// catalogoDeMetricas é a lista FECHADA do que o ficheiro contém, pela ordem em que é escrito. Uma
// linha do ficheiro anterior cujo nome não esteja aqui é descartada — o ficheiro só transporta o
// que este código sabe escrever.
var catalogoDeMetricas = []struct{ nome, tipo, ajuda string }{
	{metricaDrenagens, "counter", "Invocacoes do consume, por resultado (ok|erro)."},
	{metricaReclamados, "counter", "Pedidos de plano reclamados ao no."},
	{metricaRetomas, "counter", "Pedidos reclamados numa geracao maior do que 1 (retomas)."},
	{metricaOrigem, "counter", "Por onde entrou cada pedido: decomposicao, documento (retoma pelo plano validado), reverificacao (a espera de humano, sem serve) ou sem_serve (recusado antes do serve)."},
	{metricaDesfechos, "counter", "Desfechos por classe (terminal|transitorio|aguarda_humano) e codigo de saida do serve."},
	{metricaNaoReportados, "counter", "Desfechos que o no nao registou; o pedido volta a fila quando a reclamacao expirar."},
	{metricaDuracao, "summary", "Duracao de cada pedido (escolha da origem + serve), por classe do desfecho."},
	{metricaFalhasConsecutivas, "gauge", "Desfechos falhados seguidos (aguarda_humano e o 8, nos em voo, sao neutros); volta a 0 no primeiro terminal/0."},
	{metricaUltimaDrenagem, "gauge", "Fim da ultima drenagem, em segundos unix."},
	{metricaUltimaPedidos, "gauge", "Pedidos tratados na ultima drenagem (consumidos e re-verificados)."},
}

// Origens de um pedido — o rótulo `origem` de [metricaOrigem] e do resumo do desfecho.
const (
	origemDecomposicao  = "decomposicao"
	origemDocumento     = "documento"
	origemReverificacao = "reverificacao"
	origemSemServe      = "sem_serve"
)

// metricasDoConsumo é o estado acumulado das séries, por chave canónica `nome{rotulos}`.
type metricasDoConsumo struct {
	series map[string]float64
}

// serie constrói a chave canónica de uma série. Os rótulos vêm aos pares (nome, valor), por uma
// ordem fixa por quem chama — a mesma série tem sempre a mesma chave.
func serie(nome string, rotulos ...string) string {
	if len(rotulos) == 0 {
		return nome
	}
	var b strings.Builder
	b.WriteString(nome)
	b.WriteByte('{')
	for i := 0; i+1 < len(rotulos); i += 2 {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(rotulos[i])
		b.WriteString(`="`)
		b.WriteString(escaparRotulo(rotulos[i+1]))
		b.WriteByte('"')
	}
	b.WriteByte('}')
	return b.String()
}

// escaparRotulo aplica o escape do formato de texto do Prometheus ao valor de um rótulo.
func escaparRotulo(v string) string {
	return strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`).Replace(v)
}

// nomeDaSerie devolve o nome da métrica a que uma chave pertence — o de um summary, sem o sufixo
// `_sum`/`_count`.
func nomeDaSerie(chave string) string {
	nome := chave
	if i := strings.IndexByte(chave, '{'); i >= 0 {
		nome = chave[:i]
	}
	for _, s := range []string{"_sum", "_count"} {
		if base, ok := strings.CutSuffix(nome, s); ok && tipoDaMetrica(base) == "summary" {
			return base
		}
	}
	return nome
}

func tipoDaMetrica(nome string) string {
	for _, m := range catalogoDeMetricas {
		if m.nome == nome {
			return m.tipo
		}
	}
	return ""
}

// lerMetricas lê o ficheiro de uma drenagem anterior. Um ficheiro que não existe é o início da
// história (sem erro); um que não se lê ou não se entende devolve erro, e quem chama recomeça do
// zero dizendo-o.
func lerMetricas(caminho string) (*metricasDoConsumo, error) {
	m := &metricasDoConsumo{series: map[string]float64{}}
	raw, err := os.ReadFile(caminho)
	if errors.Is(err, fs.ErrNotExist) {
		return m, nil
	}
	if err != nil {
		return m, err
	}
	sc := bufio.NewScanner(bytes.NewReader(raw))
	for n := 1; sc.Scan(); n++ {
		linha := strings.TrimSpace(sc.Text())
		if linha == "" || strings.HasPrefix(linha, "#") {
			continue
		}
		i := strings.LastIndexByte(linha, ' ')
		if i <= 0 {
			return &metricasDoConsumo{series: map[string]float64{}}, fmt.Errorf("linha %d sem valor", n)
		}
		chave := linha[:i]
		v, err := strconv.ParseFloat(linha[i+1:], 64)
		if err != nil {
			return &metricasDoConsumo{series: map[string]float64{}}, fmt.Errorf("linha %d: valor ilegivel: %w", n, err)
		}
		if tipoDaMetrica(nomeDaSerie(chave)) == "" {
			continue // não é deste catálogo: não se transporta
		}
		m.series[chave] = v
	}
	if err := sc.Err(); err != nil {
		return &metricasDoConsumo{series: map[string]float64{}}, err
	}
	return m, nil
}

func (m *metricasDoConsumo) somar(chave string, v float64) { m.series[chave] += v }
func (m *metricasDoConsumo) fixar(chave string, v float64) { m.series[chave] = v }

// registarReclamacao conta um pedido reclamado — antes de se saber como acaba, para que um pedido
// cuja drenagem aborta a meio também conte.
func (m *metricasDoConsumo) registarReclamacao(geracao int) {
	m.somar(metricaReclamados, 1)
	if geracao > 1 {
		m.somar(metricaRetomas, 1)
	}
}

// registarDesfecho conta o desfecho de um pedido.
//
// A SÉRIE QUE O SENSOR LÊ é [metricaFalhasConsecutivas] — ver [efeitoNasFalhas] para a regra.
func (m *metricasDoConsumo) registarDesfecho(r resumoDoPedido, classe string, codigo int, reportado bool) {
	m.somar(serie(metricaOrigem, "origem", r.origem), 1)
	m.somar(serie(metricaDesfechos, "classe", classe, "codigo", strconv.Itoa(codigo)), 1)
	m.somar(serie(metricaDuracao+"_sum", "classe", classe), r.duracao.Seconds())
	m.somar(serie(metricaDuracao+"_count", "classe", classe), 1)
	if !reportado {
		m.somar(metricaNaoReportados, 1)
	}
	switch efeitoNasFalhas(classe, codigo) {
	case zeraFalhas:
		m.fixar(metricaFalhasConsecutivas, 0)
	case somaFalha:
		m.somar(metricaFalhasConsecutivas, 1)
	default:
		m.somar(metricaFalhasConsecutivas, 0) // presente no ficheiro, mesmo sem falhas
	}
}

// Efeito de um desfecho no contador de falhas seguidas.
const (
	neutroNasFalhas = iota
	zeraFalhas
	somaFalha
)

// efeitoNasFalhas é a regra do contador que o `alerta-nhi.sh` lê.
//
//   - terminal/0 ZERA: o plano correu.
//   - NEUTROS (nem somam nem zeram): `aguarda_humano` — esperar por um humano não é falha — e o
//     transitório 8 (`exitNosEmVoo`), que é o caminho FELIZ de um plano mais longo do que o
//     `--plan-timeout`: a drenagem seguinte retoma-o pelo documento (AOS-442). Contá-lo faria três
//     drenagens de um plano longo e saudável dispararem o aviso.
//   - SOMAM todos os outros: os transitórios de posse/WAL (3, 4, 5) e o genérico (1) — um só é
//     contenção normal, três seguidos já não —, e os terminais ≠ 0.
//
// O 7 (`exitDecisaoRecusada`) SOMA, e é o caso ambíguo: é a governação a funcionar quando um humano
// recusa, mas é também o validado-sem-documento e o pendente fora do prazo — um plano perdido. Não
// há como os distinguir pelo código, e contar uma recusa humana como falha custa, no pior caso, um
// aviso a mais que o operador lê como «três recusas seguidas»; não contar calaria planos perdidos.
func efeitoNasFalhas(classe string, codigo int) int {
	switch {
	case classe == "terminal" && codigo == exitOK:
		return zeraFalhas
	case classe == "aguarda_humano", codigo == exitNosEmVoo:
		return neutroNasFalhas
	default:
		return somaFalha
	}
}

// registarDrenagem fecha a contagem de uma invocação.
func (m *metricasDoConsumo) registarDrenagem(resultado string, tratados int, fim time.Time) {
	m.somar(serie(metricaDrenagens, "resultado", resultado), 1)
	m.somar(metricaFalhasConsecutivas, 0)
	m.fixar(metricaUltimaDrenagem, float64(fim.Unix()))
	m.fixar(metricaUltimaPedidos, float64(tratados))
}

// texto devolve o ficheiro em formato de texto de exposição do Prometheus, pela ordem do catálogo
// e, dentro de cada métrica, pela ordem das chaves — o mesmo estado dá sempre os mesmos bytes.
func (m *metricasDoConsumo) texto() []byte {
	porNome := map[string][]string{}
	for chave := range m.series {
		n := nomeDaSerie(chave)
		porNome[n] = append(porNome[n], chave)
	}
	var b bytes.Buffer
	for _, met := range catalogoDeMetricas {
		chaves := porNome[met.nome]
		if len(chaves) == 0 {
			continue
		}
		sort.Strings(chaves)
		fmt.Fprintf(&b, "# HELP %s %s\n# TYPE %s %s\n", met.nome, met.ajuda, met.nome, met.tipo)
		for _, c := range chaves {
			fmt.Fprintf(&b, "%s %s\n", c, strconv.FormatFloat(m.series[c], 'f', -1, 64))
		}
	}
	return b.Bytes()
}

// escreverMetricas substitui o ficheiro de forma ATÓMICA: escreve um temporário na mesma pasta,
// sincroniza-o e renomeia-o por cima. Um leitor vê o ficheiro anterior ou o novo, nunca meio.
func escreverMetricas(caminho string, conteudo []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(caminho), "."+filepath.Base(caminho)+".*")
	if err != nil {
		return err
	}
	nomeTmp := tmp.Name()
	falhar := func(e error) error {
		_ = tmp.Close()
		_ = os.Remove(nomeTmp)
		return e
	}
	if _, err := tmp.Write(conteudo); err != nil {
		return falhar(err)
	}
	if err := tmp.Sync(); err != nil {
		return falhar(err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(nomeTmp)
		return err
	}
	// 0644: o ficheiro não leva identificadores (ver o cabeçalho), e quem o copia para fora do
	// volume lê-o com outro uid.
	if err := os.Chmod(nomeTmp, 0o644); err != nil {
		_ = os.Remove(nomeTmp)
		return err
	}
	if err := os.Rename(nomeTmp, caminho); err != nil {
		_ = os.Remove(nomeTmp)
		return err
	}
	return nil
}

// caminhoDasMetricas resolve onde se escreve o ficheiro: o explícito, ou ao lado do WAL. Sobre o
// substrato replicado não há WAL de onde o derivar, e sem `--metrics-file` não se escreve — dito no
// stderr, porque é observabilidade a menos e não um erro da drenagem.
func caminhoDasMetricas(explicito string, sub substrato) string {
	if explicito != "" {
		return explicito
	}
	if sub.wal != "" {
		return filepath.Join(filepath.Dir(sub.wal), nomeDoFicheiroDeMetricas)
	}
	fmt.Fprintln(os.Stderr, "aos-orq: consume sobre --nats sem --metrics-file: esta drenagem nao escreve metricas (AOS-443)")
	return ""
}

// resumoDoPedido é o que se diz de UM pedido, no `detail` do desfecho e na linha `desfecho:` do
// stdout — também em sucesso, que era o que faltava: «terminado» e «terminado com sucesso» diziam
// o mesmo (ADR-031 §4.1, AOS-443).
//
// SÓ IDS, CONTAGENS, CÓDIGOS E DURAÇÕES. O `detail` fica gravado no nó e é servido pelo
// `GET /plans/{id}`; o objectivo e o resultado são dados do titular e não entram aqui.
type resumoDoPedido struct {
	origem  string
	geracao int
	// nos é o número de nós do documento do plano; -1 quando não se sabe (o `serve` falhou antes de
	// haver documento, ou o que está no disco não é deste pedido).
	nos     int
	duracao time.Duration
	// erro é o TIPO do erro do `serve` ([tipoDoErro]) — um nome de vocabulário fechado, nunca o
	// texto do erro; vazio sem erro.
	erro string
}

// linha é o formato do resumo: pares `chave=valor` separados por espaço, sem aspas, para se ler a
// olho e se cortar com `grep`/`awk`. `nos=-` quando não se sabe; `erro=<tipo>` só quando houve.
func (r resumoDoPedido) linha() string {
	nos := "-"
	if r.nos >= 0 {
		nos = strconv.Itoa(r.nos)
	}
	l := fmt.Sprintf("origem=%s geracao=%d nos=%s duracao_s=%s",
		r.origem, r.geracao, nos, strconv.FormatFloat(r.duracao.Seconds(), 'f', 3, 64))
	if r.erro != "" {
		l += " erro=" + r.erro
	}
	return l
}

// detalheDoDesfecho é o `detail` que se reporta ao nó. Só o resumo: tem tamanho limitado (bem
// abaixo dos 512 bytes a que o nó trunca — `truncar` em packages/cmd/aos/plan_claim.go) e não leva
// texto livre nenhum.
func detalheDoDesfecho(r resumoDoPedido) string {
	return "resumo: " + r.linha()
}

// origemDoResumo classifica por onde o pedido entrou. `serveCorreu` distingue os desfechos que o
// `serve` produziu dos que a escolha da origem produziu sem ele (ver retoma_do_plano.go).
func origemDoResumo(o origemDoPlano, serveCorreu bool, classe string) string {
	switch {
	case !serveCorreu && o.jaValidado && classe == "aguarda_humano":
		return origemReverificacao
	case !serveCorreu:
		return origemSemServe
	case o.porDocumento:
		return origemDocumento
	default:
		return origemDecomposicao
	}
}

// nosDoDocumento conta os nós do documento do plano deste pedido, ou -1.
//
// Um documento que já estava no disco antes deste pedido e que o log não ancora NÃO conta: é o
// caso do documento plantado que o AOS-442 recusa usar, e contá-lo diria um número que não é o do
// plano que correu. Ancorado quer dizer que a escolha da origem o confrontou com o `plan.validated`
// do run; senão, só conta se foi escrito durante este pedido (o `--plan-out` do `serve`).
func nosDoDocumento(doc string, ancorado bool, desde time.Time) int {
	if doc == "" {
		return -1
	}
	info, err := os.Stat(doc)
	if err != nil {
		return -1
	}
	if !ancorado && info.ModTime().Before(desde.Truncate(time.Second)) {
		return -1
	}
	raw, err := os.ReadFile(doc)
	if err != nil {
		return -1
	}
	d, err := plan.Decode(raw)
	if err != nil {
		return -1
	}
	return len(d.Nodes)
}
