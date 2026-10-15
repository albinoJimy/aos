package bancoensaio

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// A EXPERIÊNCIA `ocr` (AOS-529, fase A5, «A5-ocr-medição»). Corre um MOTOR de OCR sobre cada
// imagem da bateria de OCR e mede, por imagem: os factos presentes (pela regra dos factos do
// banco, e também com os acentos exactos), o tempo, a memória máxima, o tamanho do texto e, num
// motor que é um modelo, os tokens e o custo estimado. Não dá veredicto: o limiar de 90% do
// desenho é uma proposta, e o relatório só o mostra ao lado.
//
// # O MOTOR É PLUGÁVEL
//
// Um motor é só [MotorOCR]: recebe uma imagem e devolve texto. O tempo mede-o a experiência, por
// fora, à volta da chamada; a memória mede-a quem corre o motor (o contentor, pelo cgroup), não o
// motor. Acrescentar um candidato — EasyOCR, RapidOCR, surya, Docling — é escrever um adaptador
// que cumpra a interface e dar-lhe um nome na linha de comandos ([MotoresDeOCR]). Hoje há três:
// o Tesseract num contentor fixado, um modelo de visão pelo proxy do banco (ocr_visao.go), e o
// falso.
//
// # O TEXTO LIDO NÃO SAI DAQUI
//
// O texto que o motor devolve serve só para contar os factos. Não vai para o relatório, o resumo
// nem as saídas: só contagens, os factos sintéticos ausentes e o tamanho. Com `--guardar-respostas`
// fica num ficheiro por imagem, ao lado do relatório, e só lá.

// Os limites de uma chamada de OCR, os do executor gVisor de produção: 45 s
// (`deploy/server/gvisor/component/main.go`, `GV_EXEC_TIMEOUT`), 1 GiB (o `mem_limit` do serviço
// `gvisor` em `deploy/server/docker-compose.prod.yml`) e nenhuma rede (`--network=none`).
const (
	LimiteDeTempoDoOCR    = 45 * time.Second
	LimiteDeMemoriaDoOCR  = int64(1) << 30
	LimiteDeProcessosOCR  = 512
	PropostaDeFactosDoOCR = 0.90
)

// MotorOCR é um motor de OCR. Ler devolve o texto lido da imagem, ou um erro: [ErrOCRTempo] e
// [ErrOCRMemoria] dizem que a chamada passou um limite; [ErrOCRFora], que a imagem está fora do
// motor (não conta); um [ParagemDoOCR], que a corrida tem de parar.
type MotorOCR interface {
	// Nome identifica o motor no relatório (por exemplo `tesseract`, `falso-certo`).
	Nome() string
	// Descricao diz o que mais identifica o motor: a imagem do contentor e a versão, ou o modelo.
	Descricao() string
	// Condicoes diz os limites com que cada chamada corre e o que a medição deste motor não prova.
	Condicoes() CondicoesDoMotor
	Ler(ctx context.Context, m MediaOCR) (LeituraOCR, error)
}

// CondicoesDoMotor são os limites de uma chamada e as notas próprias do motor.
type CondicoesDoMotor struct {
	Limites LimitesDoOCR
	Notas   []string
}

// LeituraOCR é o que um motor devolve de uma imagem.
type LeituraOCR struct {
	Texto string
	// MemoriaPico é a memória máxima da chamada, em bytes, medida por quem corre o motor; -1 se
	// não foi medida.
	MemoriaPico int64
	// Tokens e custo, num motor que é um modelo. CustoMicroUSD é -1 sem preço declarado.
	TokensDeEntrada, TokensDeSaida int64
	CustoMicroUSD                  int64
	// ComCusto diz que o motor é um modelo e que os tokens e o custo se contam.
	ComCusto bool
}

// Os erros de uma leitura.
var (
	ErrOCRTempo   = errors.New("banco-ensaio: a leitura de OCR passou o limite de tempo")
	ErrOCRMemoria = errors.New("banco-ensaio: a leitura de OCR passou o limite de memoria")
	// ErrOCRFora — a imagem está fora deste motor (por exemplo o PDF no modelo de visão): fica com o
	// estado `fora`, e os factos dela não entram nas contas.
	ErrOCRFora = errors.New("banco-ensaio: imagem fora deste motor")
)

// ParagemDoOCR é o erro de uma leitura que pára a corrida: o tecto do dia, o contador, ou uma
// recusa do fornecedor que se repetiria em todas as imagens.
type ParagemDoOCR struct{ Causa string }

func (p *ParagemDoOCR) Error() string { return "banco-ensaio: a corrida de OCR parou: " + p.Causa }

// Os estados e as causas de uma imagem.
const (
	EstadoOCRLida   = "lida"
	EstadoOCRFalhou = "falhou"
	EstadoOCRFora   = "fora"

	CausaOCRTempo   = "tempo"
	CausaOCRMemoria = "memoria"
	CausaOCRMotor   = "motor"
	CausaOCRFormato = "formato"
)

// RelatorioOCR é o relatório da experiência `ocr`.
type RelatorioOCR struct {
	Experiencia    string       `json:"experiencia"`
	DataUTC        string       `json:"data_utc"`
	VersaoBateria  string       `json:"versao_bateria"`
	DigestBateria  string       `json:"digest_bateria"`
	Motor          string       `json:"motor"`
	DescricaoMotor string       `json:"descricao_motor"`
	Limites        LimitesDoOCR `json:"limites"`
	Interrompida   bool         `json:"interrompida,omitempty"`
	// Paragem é a causa por que a corrida parou antes do fim (tecto, contador, recusa).
	Paragem string             `json:"paragem,omitempty"`
	Imagens []LeituraDaImagem  `json:"imagens"`
	Resumo  []LinhaDoResumoOCR `json:"resumo"`
	// LimitesDaMedicao são os limites declarados da própria medição.
	LimitesDaMedicao []string `json:"limites_da_medicao"`

	// Os quatro abaixo só existem num motor que é um modelo pago (o de visão). Têm os nomes dos
	// campos dos relatórios das corridas reais, para que o contador do dia se reconstrua também a
	// partir deste relatório (`UsoDosRelatoriosDeHoje`).
	Modo    string        `json:"modo,omitempty"`
	Rota    *RotaDoOCR    `json:"rota,omitempty"`
	Pedidos *PedidosDoOCR `json:"pedidos,omitempty"`
	Custo   *CustoDoOCR   `json:"custo,omitempty"`
}

// RotaDoOCR é o fornecedor e o modelo de um motor que é um modelo.
type RotaDoOCR struct {
	Fornecedor string `json:"fornecedor"`
	Modelo     string `json:"modelo"`
}

// PedidosDoOCR são os pedidos que saíram para o fornecedor (com a sonda).
type PedidosDoOCR struct {
	Enviados int64  `json:"enviados"`
	PiorCaso int64  `json:"pior_caso"`
	DiaUTC   string `json:"dia_utc"`
}

// CustoDoOCR é o custo estimado: o da corrida e o do dia no contador do fornecedor.
type CustoDoOCR struct {
	MicroUSDDaCorrida *int64 `json:"micro_usd_estimado_da_corrida,omitempty"`
	MicroUSDHoje      *int64 `json:"micro_usd_estimado_hoje,omitempty"`
}

// LimitesDoOCR são os limites com que cada chamada correu.
type LimitesDoOCR struct {
	TempoPorChamadaS   int    `json:"tempo_por_chamada_s"`
	MemoriaPorChamadaB int64  `json:"memoria_por_chamada_bytes"`
	Rede               string `json:"rede"`
}

// limitesDoGVisor são os limites do executor gVisor de produção.
var limitesDoGVisor = LimitesDoOCR{TempoPorChamadaS: int(LimiteDeTempoDoOCR / time.Second), MemoriaPorChamadaB: LimiteDeMemoriaDoOCR, Rede: "none"}

// LeituraDaImagem é o resultado de uma imagem.
type LeituraDaImagem struct {
	ID         string `json:"id"`
	Documento  string `json:"documento"`
	Degradacao string `json:"degradacao"`
	Formato    string `json:"formato"`
	ComAcentos bool   `json:"com_acentos"`
	Bytes      int    `json:"bytes"`
	Largura    int    `json:"largura"`
	Altura     int    `json:"altura"`
	// DigestDoMedia é o sha256 do ficheiro que o motor leu.
	DigestDoMedia string `json:"digest_do_media"`
	// Estado é `lida`, `falhou` ou `fora`; Causa diz porquê, quando não foi lida.
	Estado string `json:"estado"`
	Causa  string `json:"causa,omitempty"`
	Factos int    `json:"factos"`
	// PresentesPelaRegra conta os factos pela regra do banco (sem acentos nem maiúsculas);
	// PresentesComAcentos, com os acentos exactos.
	PresentesPelaRegra  int      `json:"presentes_pela_regra"`
	PresentesComAcentos int      `json:"presentes_com_acentos"`
	AusentesPelaRegra   []string `json:"ausentes_pela_regra,omitempty"`
	SemOAcentoExacto    []string `json:"sem_o_acento_exacto,omitempty"`
	TempoMs             int64    `json:"tempo_ms"`
	MemoriaPicoBytes    int64    `json:"memoria_pico_bytes"`
	TamanhoDoTexto      int      `json:"tamanho_do_texto"`
	TokensDeEntrada     int64    `json:"tokens_de_entrada,omitempty"`
	TokensDeSaida       int64    `json:"tokens_de_saida,omitempty"`
	// CustoMicroUSD é o custo estimado da imagem, num motor que é um modelo com preço declarado.
	CustoMicroUSD *int64 `json:"custo_micro_usd_estimado,omitempty"`
}

// LinhaDoResumoOCR agrega as imagens de um grupo (uma degradação, um formato, com ou sem acentos,
// ou o total). As imagens `fora` contam-se à parte e não entram nos factos.
type LinhaDoResumoOCR struct {
	Grupo               string `json:"grupo"`
	Imagens             int    `json:"imagens"`
	Fora                int    `json:"fora,omitempty"`
	Lidas               int    `json:"lidas"`
	Factos              int    `json:"factos"`
	PresentesPelaRegra  int    `json:"presentes_pela_regra"`
	PresentesComAcentos int    `json:"presentes_com_acentos"`
	TempoMaximoMs       int64  `json:"tempo_maximo_ms"`
	MemoriaMaximaBytes  int64  `json:"memoria_maxima_bytes"`
	CustoMicroUSD       *int64 `json:"custo_micro_usd_estimado,omitempty"`
}

// limitesDaMedicao são ditos em todos os relatórios: os da bateria e os do motor.
func limitesDaMedicao(b *BateriaOCR, m MotorOCR) []string {
	return append([]string{
		fmt.Sprintf("imagens sinteticas: %d documentos, %d imagens; nenhum documento real", len(b.Documentos), len(b.Imagens)),
		"uma fonte so, bitmap 5x7 desenhada para o banco, sem serifas; um documento impresso tem fontes vectoriais. Pode favorecer ou desfavorecer um motor face a outro: vale para a COMPARACAO entre motores, nao so para a taxa de cada um",
		"degradacoes simuladas (ruido, rotacao de 1,5 graus, meia resolucao, JPEG de qualidade 55); nenhuma foto inclinada nem dobra de papel",
		"sem veredicto: o limiar de 90% dos factos e uma proposta do desenho, nao uma medida",
	}, m.Condicoes().Notas...)
}

// CorrerExperienciaOCR corre o motor sobre todas as imagens da bateria. Uma imagem que falha não
// pára a corrida: fica com o estado `falhou` e a causa, e os factos dela contam como ausentes.
// Um contexto cancelado, ou um [ParagemDoOCR], pára entre duas imagens, e o relatório sai
// `interrompida`. `guardar`, quando presente, recebe o texto lido de cada imagem (`--guardar-respostas`).
func CorrerExperienciaOCR(ctx context.Context, b *BateriaOCR, motor MotorOCR, relogio func() time.Time, guardar func(imagem, texto string)) *RelatorioOCR {
	if relogio == nil {
		relogio = time.Now
	}
	r := &RelatorioOCR{
		Experiencia: "ocr", DataUTC: relogio().UTC().Format(time.RFC3339),
		VersaoBateria: b.Versao, DigestBateria: b.Digest(),
		Motor: motor.Nome(), DescricaoMotor: motor.Descricao(),
		Limites: motor.Condicoes().Limites, LimitesDaMedicao: limitesDaMedicao(b, motor),
	}
	for _, m := range b.Media() {
		if ctx.Err() != nil {
			r.Interrompida = true
			break
		}
		d, _ := b.documento(m.Documento)
		l := LeituraDaImagem{
			ID: m.ID, Documento: m.Documento, Degradacao: m.Degradacao, Formato: m.Formato,
			ComAcentos: d.ComAcentos(), Bytes: len(m.Bytes), Largura: m.Largura, Altura: m.Altura,
			DigestDoMedia: m.Digest, Factos: len(d.Factos), MemoriaPicoBytes: -1,
		}
		inicio := relogio()
		lida, err := motor.Ler(ctx, m)
		l.TempoMs = relogio().Sub(inicio).Milliseconds()
		if lida.ComCusto {
			l.TokensDeEntrada, l.TokensDeSaida = lida.TokensDeEntrada, lida.TokensDeSaida
			if lida.CustoMicroUSD >= 0 {
				c := lida.CustoMicroUSD
				l.CustoMicroUSD = &c
			}
		}
		var paragem *ParagemDoOCR
		switch {
		case err != nil && ctx.Err() != nil && !errors.Is(err, ErrOCRTempo):
			r.Interrompida = true
		case errors.As(err, &paragem):
			r.Interrompida, r.Paragem = true, paragem.Causa
		case errors.Is(err, ErrOCRFora):
			l.Estado, l.Causa = EstadoOCRFora, CausaOCRFormato
		case err != nil:
			l.Estado, l.Causa = EstadoOCRFalhou, causaDoErroOCR(err)
			l.AusentesPelaRegra = append([]string(nil), d.Factos...)
		default:
			l.Estado = EstadoOCRLida
			l.MemoriaPicoBytes = lida.MemoriaPico
			l.TamanhoDoTexto = len(lida.Texto)
			contarFactos(&l, lida.Texto, d.Factos)
			if guardar != nil {
				guardar(m.ID, lida.Texto)
			}
		}
		if r.Interrompida {
			break
		}
		r.Imagens = append(r.Imagens, l)
	}
	r.Resumo = resumoOCR(r.Imagens)
	return r
}

// contarFactos conta, numa leitura, os factos presentes pela regra e com os acentos exactos.
func contarFactos(l *LeituraDaImagem, texto string, factos []string) {
	for _, f := range factos {
		pelaRegra := factoNaSaida(texto, f)
		if pelaRegra {
			l.PresentesPelaRegra++
		} else {
			l.AusentesPelaRegra = append(l.AusentesPelaRegra, f)
		}
		if factoComAcentosNaSaida(texto, f) {
			l.PresentesComAcentos++
		} else if pelaRegra {
			l.SemOAcentoExacto = append(l.SemOAcentoExacto, f)
		}
	}
}

func causaDoErroOCR(err error) string {
	switch {
	case errors.Is(err, ErrOCRTempo):
		return CausaOCRTempo
	case errors.Is(err, ErrOCRMemoria):
		return CausaOCRMemoria
	}
	return CausaOCRMotor
}

// resumoOCR agrega por degradação, por formato, por acentos, e no total — por esta ordem.
func resumoOCR(imagens []LeituraDaImagem) []LinhaDoResumoOCR {
	grupos := map[string]*LinhaDoResumoOCR{}
	var ordem []string
	somar := func(nome string, l LeituraDaImagem) {
		g, ok := grupos[nome]
		if !ok {
			g = &LinhaDoResumoOCR{Grupo: nome, MemoriaMaximaBytes: -1}
			grupos[nome] = g
			ordem = append(ordem, nome)
		}
		if l.Estado == EstadoOCRFora {
			g.Fora++
			return
		}
		g.Imagens++
		g.Factos += l.Factos
		if l.Estado == EstadoOCRLida {
			g.Lidas++
		}
		g.PresentesPelaRegra += l.PresentesPelaRegra
		g.PresentesComAcentos += l.PresentesComAcentos
		if l.TempoMs > g.TempoMaximoMs {
			g.TempoMaximoMs = l.TempoMs
		}
		if l.MemoriaPicoBytes > g.MemoriaMaximaBytes {
			g.MemoriaMaximaBytes = l.MemoriaPicoBytes
		}
		if l.CustoMicroUSD != nil {
			soma := *l.CustoMicroUSD
			if g.CustoMicroUSD != nil {
				soma += *g.CustoMicroUSD
			}
			g.CustoMicroUSD = &soma
		}
	}
	for _, chave := range []func(LeituraDaImagem) string{
		func(l LeituraDaImagem) string { return "degradacao " + l.Degradacao },
		func(l LeituraDaImagem) string { return "formato " + l.Formato },
		func(l LeituraDaImagem) string {
			if l.ComAcentos {
				return "com acentos"
			}
			return "sem acentos"
		},
	} {
		var nomes []string
		porNome := map[string][]LeituraDaImagem{}
		for _, l := range imagens {
			n := chave(l)
			if _, ok := porNome[n]; !ok {
				nomes = append(nomes, n)
			}
			porNome[n] = append(porNome[n], l)
		}
		sort.Strings(nomes)
		for _, n := range nomes {
			for _, l := range porNome[n] {
				somar(n, l)
			}
		}
	}
	for _, l := range imagens {
		somar("total", l)
	}
	out := make([]LinhaDoResumoOCR, 0, len(ordem))
	for _, n := range ordem {
		out = append(out, *grupos[n])
	}
	return out
}

func percentagem(n, d int) string {
	if d == 0 {
		return "  -  "
	}
	return fmt.Sprintf("%5.1f%%", 100*float64(n)/float64(d))
}

func mib(b int64) string {
	if b < 0 {
		return "n/m"
	}
	return fmt.Sprintf("%.0f MiB", float64(b)/(1<<20))
}

func usd(c *int64) string {
	if c == nil {
		return "-"
	}
	return fmt.Sprintf("%.4f USD", float64(*c)/1e6)
}

// ResumoOCREmTexto é o resumo legível: o motor, uma tabela por imagem, a tabela curta por grupo e
// os limites declarados. Nunca leva o texto lido.
func ResumoOCREmTexto(r *RelatorioOCR) string {
	var b strings.Builder
	fmt.Fprintf(&b, "experiencia ocr (AOS-529) — %s\n", r.DataUTC)
	fmt.Fprintf(&b, "bateria de OCR %s  digest %s\n", r.VersaoBateria, r.DigestBateria)
	fmt.Fprintf(&b, "motor %s  (%s)\n", r.Motor, r.DescricaoMotor)
	fmt.Fprintf(&b, "limites por chamada: %d s, %s, rede %s\n", r.Limites.TempoPorChamadaS, mib(r.Limites.MemoriaPorChamadaB), r.Limites.Rede)
	if r.Pedidos != nil {
		fmt.Fprintf(&b, "pedidos ao fornecedor: %d (pior caso declarado %d, com a sonda)", r.Pedidos.Enviados, r.Pedidos.PiorCaso)
		if r.Custo != nil {
			fmt.Fprintf(&b, "; custo estimado da corrida %s", usd(r.Custo.MicroUSDDaCorrida))
		}
		b.WriteString("\n")
	}
	if r.Interrompida {
		b.WriteString("INTERROMPIDA: a corrida parou antes de ler todas as imagens")
		if r.Paragem != "" {
			fmt.Fprintf(&b, " (%s)", r.Paragem)
		}
		b.WriteString("\n")
	}
	b.WriteString("\npor imagem:\n")
	fmt.Fprintf(&b, "  %-20s %-16s %-5s %-7s %-15s %7s %7s %8s %9s %7s %12s\n", "imagem", "degradacao", "fmt", "acentos", "estado", "regra", "acentos", "tempo", "memoria", "texto", "custo")
	for _, l := range r.Imagens {
		estado := l.Estado
		if l.Causa != "" {
			estado += ":" + l.Causa
		}
		acentos := "nao"
		if l.ComAcentos {
			acentos = "sim"
		}
		fmt.Fprintf(&b, "  %-20s %-16s %-5s %-7s %-15s %3d/%-3d %3d/%-3d %6dms %9s %6dB %12s\n", l.ID, l.Degradacao, l.Formato, acentos, estado,
			l.PresentesPelaRegra, l.Factos, l.PresentesComAcentos, l.Factos, l.TempoMs, mib(l.MemoriaPicoBytes), l.TamanhoDoTexto, usd(l.CustoMicroUSD))
	}
	b.WriteString("\nresumo (factos presentes; regra = sem acentos nem maiusculas, como o banco; as imagens fora do motor nao contam):\n")
	fmt.Fprintf(&b, "  %-26s %7s %5s %7s %7s %8s %9s %12s\n", "grupo", "lidas", "fora", "regra", "acentos", "t. max", "mem. max", "custo")
	for _, g := range r.Resumo {
		fmt.Fprintf(&b, "  %-26s %3d/%-3d %5d %7s %7s %6dms %9s %12s\n", g.Grupo, g.Lidas, g.Imagens, g.Fora,
			percentagem(g.PresentesPelaRegra, g.Factos), percentagem(g.PresentesComAcentos, g.Factos), g.TempoMaximoMs, mib(g.MemoriaMaximaBytes), usd(g.CustoMicroUSD))
	}
	fmt.Fprintf(&b, "\nproposta do desenho (nao e veredicto): pelo menos %.0f%% dos factos presentes na bateria\n", 100*PropostaDeFactosDoOCR)
	b.WriteString("\nlimites desta medicao:\n")
	for _, l := range r.LimitesDaMedicao {
		fmt.Fprintf(&b, "  - %s\n", l)
	}
	return b.String()
}

// EscreverRelatorioOCR grava o relatório e o resumo na pasta e devolve os dois caminhos. O nome
// é `ensaio-ocr-<motor>-<data>`; num motor que é um modelo pago, `ensaio-real-ocr-<motor>-<data>`,
// para entrar na reconstrução do contador do dia como as outras corridas reais.
func EscreverRelatorioOCR(pasta string, r *RelatorioOCR) (string, string, error) {
	if err := os.MkdirAll(pasta, 0o700); err != nil {
		return "", "", fmt.Errorf("banco-ensaio: pasta dos relatorios: %w", err)
	}
	carimbo := strings.NewReplacer("-", "", ":", "").Replace(r.DataUTC)
	prefixo := "ensaio-ocr-"
	if r.Modo == ModoReal {
		prefixo = "ensaio-" + ModoReal + "-ocr-"
	}
	base := filepath.Join(pasta, prefixo+r.Motor+"-"+carimbo)
	cru, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return "", "", err
	}
	if err := os.WriteFile(base+".json", append(cru, '\n'), 0o600); err != nil {
		return "", "", err
	}
	if err := os.WriteFile(base+".txt", []byte(ResumoOCREmTexto(r)), 0o600); err != nil {
		return "", "", err
	}
	return base + ".json", base + ".txt", nil
}

// FormatoDasLeiturasGuardadas é o formato de cada ficheiro das leituras guardadas.
const FormatoDasLeiturasGuardadas = "aos-ensaio/leitura-ocr/v1"

// EscreverLeiturasGuardadas grava, ao lado do relatório, a pasta `respostas-<nome do relatório>/`
// com um ficheiro JSON por imagem: o texto lido. Recusa escrever numa pasta que já exista.
func EscreverLeiturasGuardadas(relatorioEmJSON, motor string, textos map[string]string) (string, error) {
	nome := strings.TrimSuffix(filepath.Base(relatorioEmJSON), ".json")
	pasta := filepath.Join(filepath.Dir(relatorioEmJSON), PrefixoDaPastaDasRespostas+nome)
	if err := os.Mkdir(pasta, 0o700); err != nil {
		if errors.Is(err, os.ErrExist) {
			return pasta, ErrPastaDasRespostasExiste
		}
		return pasta, err
	}
	ids := make([]string, 0, len(textos))
	for id := range textos {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		cru, err := json.MarshalIndent(map[string]string{"formato": FormatoDasLeiturasGuardadas, "imagem": id, "motor": motor, "texto": textos[id]}, "", "  ")
		if err != nil {
			return pasta, err
		}
		if err := os.WriteFile(filepath.Join(pasta, id+".json"), append(cru, '\n'), 0o600); err != nil {
			return pasta, err
		}
	}
	return pasta, nil
}
