package bancoensaio

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// A EXPERIÊNCIA `video` (AOS-537, sub-fase A6.1 «vídeo nativo como tool», opção A do dono de
// 2026-10-18). Corre um MOTOR sobre cada vídeo da bateria de vídeo e mede, por vídeo: os factos
// presentes na descrição (pela regra dos factos do banco e, à parte, com os acentos exactos), por
// tipo de facto, o tempo e, num motor que é um modelo, os tokens e o custo estimado. É o molde da
// experiência `ocr` (AOS-529/530): o motor é plugável, o texto lido não sai do relatório, e o
// relatório traz um VEREDICTO contra o limiar de factos do dono.
//
// # O VEREDICTO
//
// O limiar é o do OCR, a D7 do dono: pelo menos 90% dos factos presentes ([LimiarDeFactosDoVideo]),
// e NENHUM vídeo por ler — o modelo da tool é um só, sem nova tentativa (como no ADR-042 §2.8), e um
// vídeo que ele não descreve é uma chamada da tool que falha. Uma resposta cortada pelo limite de
// tokens (`finish_reason: length`) é um vídeo por ler: a lição do Gemini no AOS-529.
//
// # O TEXTO LIDO NÃO SAI DAQUI
//
// A descrição do motor serve só para contar os factos. Não vai para o relatório, o resumo nem as
// saídas; só contagens, os factos sintéticos ausentes e o tamanho. Com `--guardar-respostas` fica
// num ficheiro por vídeo, ao lado do relatório, e só lá.

// O VEREDICTO DA EXPERIÊNCIA `video`.
const (
	// LimiarDeFactosDoVideo é o limiar do OCR (D7 do dono, 2026-10-15), que o dono fixou também
	// para o vídeo (2026-10-18). A opção `--limiar-de-factos` só o aperta.
	LimiarDeFactosDoVideo = LimiarDeFactosDoOCR
	// VersaoDoRelatorioVideo é a versão do formato do relatório `video`.
	VersaoDoRelatorioVideo = "1"
	// RegraDoVeredictoVideo é a regra, em texto fixo, que vai no relatório.
	RegraDoVeredictoVideo = "qualificado se a corrida completou, pelo menos um video com factos entrou nas contas, TODOS os videos foram descritos (nenhum falhou: tempo, erro do motor ou resposta cortada) e a fraccao de factos presentes pela regra do banco (sem acentos nem maiusculas; um facto de ordem exige as duas linhas e a primeira antes da segunda) e pelo menos o limiar; senao recusado. So um qualificado do modo real e a medida de um modelo"
)

// MotorDeVideo é um motor que descreve um vídeo. Descrever devolve o texto da descrição, ou um
// erro: [ErrVideoTempo] diz que a chamada passou o limite; [ErrVideoCortado], que a resposta veio
// cortada; um [ParagemDoVideo], que a corrida tem de parar.
type MotorDeVideo interface {
	// Nome identifica o motor no relatório (`visao`, `falso-certo`).
	Nome() string
	// Descricao diz o que mais identifica o motor: o modelo, a instrução.
	Descricao() string
	// Condicoes diz os limites de cada chamada e o que a medição deste motor não prova.
	Condicoes() CondicoesDoMotorDeVideo
	// PrecisaDoMP4 diz se o motor lê o MP4 (e a experiência tem de o gerar pelo ffmpeg).
	PrecisaDoMP4() bool
	Descrever(ctx context.Context, v VideoPronto) (RespostaDoVideo, error)
}

// CondicoesDoMotorDeVideo são os limites de uma chamada e as notas próprias do motor.
type CondicoesDoMotorDeVideo struct {
	TempoPorChamadaS int
	Rede             string
	Notas            []string
}

// RespostaDoVideo é o que um motor devolve de um vídeo.
type RespostaDoVideo struct {
	Texto                          string
	TokensDeEntrada, TokensDeSaida int64
	// CustoMicroUSD é -1 sem preço declarado; ComCusto diz que o motor é um modelo.
	CustoMicroUSD int64
	ComCusto      bool
}

// Os erros de uma descrição.
var (
	ErrVideoTempo   = errors.New("banco-ensaio: a descricao do video passou o limite de tempo")
	ErrVideoCortado = errors.New("banco-ensaio: a descricao do video veio cortada pelo limite de tokens")
)

// ParagemDoVideo é o erro de uma descrição que pára a corrida: o tecto do dia, o contador, ou uma
// recusa do fornecedor que se repetiria em todos os vídeos.
type ParagemDoVideo struct{ Causa string }

func (p *ParagemDoVideo) Error() string { return "banco-ensaio: a corrida de video parou: " + p.Causa }

// Os estados e as causas de um vídeo.
const (
	EstadoVideoDescrito = "descrito"
	EstadoVideoFalhou   = "falhou"

	CausaVideoTempo   = "tempo"
	CausaVideoMotor   = "motor"
	CausaVideoCortado = "cortada"
)

// RelatorioVideo é o relatório da experiência `video`.
type RelatorioVideo struct {
	VersaoDoRelatorio string `json:"versao_do_relatorio"`
	Experiencia       string `json:"experiencia"`
	DataUTC           string `json:"data_utc"`
	VersaoBateria     string `json:"versao_bateria"`
	DigestBateria     string `json:"digest_bateria"`
	Motor             string `json:"motor"`
	DescricaoMotor    string `json:"descricao_motor"`
	// Codificador é o ffmpeg que gerou os MP4 (a imagem e a versão); vazio num motor sem MP4.
	Codificador string `json:"codificador,omitempty"`
	// Instrucao é a versão e o digest da instrução fixa do motor de visão.
	Instrucao *InstrucaoDoVideoNoRelatorio `json:"instrucao,omitempty"`
	Limites   LimitesDoVideo               `json:"limites"`
	// Interrompida e Paragem: a corrida parou antes do fim (tecto, contador, recusa).
	Interrompida bool                  `json:"interrompida,omitempty"`
	Paragem      string                `json:"paragem,omitempty"`
	Videos       []LeituraDoVideo      `json:"videos"`
	PorTipo      []LinhaPorTipoDeFacto `json:"por_tipo_de_facto"`
	// LimitesDaMedicao são os limites declarados da própria medição.
	LimitesDaMedicao []string `json:"limites_da_medicao"`

	// Os quatro abaixo só existem num motor que é um modelo pago, com os nomes de campo das
	// corridas reais, para o contador do dia se reconstruir também deste relatório.
	Modo    string        `json:"modo,omitempty"`
	Rota    *RotaDoOCR    `json:"rota,omitempty"`
	Pedidos *PedidosDoOCR `json:"pedidos,omitempty"`
	Custo   *CustoDoOCR   `json:"custo,omitempty"`

	VeredictoDoVideo  *VeredictoDoVideo `json:"veredicto_do_video,omitempty"`
	DigestDoRelatorio string            `json:"digest_do_relatorio,omitempty"`
}

// InstrucaoDoVideoNoRelatorio identifica a instrução fixa com que o modelo foi chamado.
type InstrucaoDoVideoNoRelatorio struct {
	Versao string `json:"versao"`
	Digest string `json:"digest"`
	// MaxTokens é o `max_tokens` de cada pedido.
	MaxTokens int `json:"max_tokens"`
}

// LimitesDoVideo são os limites com que cada chamada correu.
type LimitesDoVideo struct {
	TempoPorChamadaS int    `json:"tempo_por_chamada_s"`
	Rede             string `json:"rede"`
}

// VeredictoDoVideo é o bloco do veredicto de uma corrida `video`.
type VeredictoDoVideo struct {
	Veredicto          string   `json:"veredicto"`
	Razoes             []string `json:"razoes"`
	Regra              string   `json:"regra"`
	LimiarDeFactos     float64  `json:"limiar_de_factos"`
	VideosNasContas    int      `json:"videos_nas_contas"`
	VideosQueFalharam  int      `json:"videos_que_falharam"`
	Factos             int      `json:"factos"`
	PresentesPelaRegra int      `json:"presentes_pela_regra"`
	// ValeParaProducao: só um `qualificado` do modo real. Nenhum nó o lê ainda: a tool de vídeo é
	// o AOS-536.
	ValeParaProducao          bool   `json:"vale_para_producao"`
	PorqueNaoValeParaProducao string `json:"porque_nao_vale_para_producao,omitempty"`
	// RotaMedida é o perfil e a rota medidos, quando a corrida foi feita com `--perfil`.
	RotaMedida *RotaMedidaDoOCR `json:"rota_medida,omitempty"`
}

// ContagemDoTipo conta os factos de um tipo num vídeo.
type ContagemDoTipo struct {
	Tipo                string `json:"tipo"`
	Factos              int    `json:"factos"`
	PresentesPelaRegra  int    `json:"presentes_pela_regra"`
	PresentesComAcentos int    `json:"presentes_com_acentos"`
}

// LeituraDoVideo é o resultado de um vídeo.
type LeituraDoVideo struct {
	ID         string `json:"id"`
	Largura    int    `json:"largura"`
	Altura     int    `json:"altura"`
	FPS        int    `json:"fps"`
	Segundos   int    `json:"segundos"`
	Fotogramas int    `json:"fotogramas"`
	Cenas      int    `json:"cenas"`
	// Bytes e DigestDoMedia são os do MP4 que o motor leu; ausentes num motor sem MP4.
	Bytes         int    `json:"bytes,omitempty"`
	DigestDoMedia string `json:"digest_do_media,omitempty"`
	Estado        string `json:"estado"`
	Causa         string `json:"causa,omitempty"`
	Factos        int    `json:"factos"`
	// PresentesPelaRegra conta os factos pela regra do banco; PresentesComAcentos, com os acentos
	// exactos.
	PresentesPelaRegra  int              `json:"presentes_pela_regra"`
	PresentesComAcentos int              `json:"presentes_com_acentos"`
	PorTipo             []ContagemDoTipo `json:"por_tipo"`
	AusentesPelaRegra   []string         `json:"ausentes_pela_regra,omitempty"`
	SemOAcentoExacto    []string         `json:"sem_o_acento_exacto,omitempty"`
	TempoMs             int64            `json:"tempo_ms"`
	TamanhoDoTexto      int              `json:"tamanho_do_texto"`
	TokensDeEntrada     int64            `json:"tokens_de_entrada,omitempty"`
	TokensDeSaida       int64            `json:"tokens_de_saida,omitempty"`
	CustoMicroUSD       *int64           `json:"custo_micro_usd_estimado,omitempty"`
}

// LinhaPorTipoDeFacto agrega um tipo de facto em todos os vídeos (ou o total).
type LinhaPorTipoDeFacto struct {
	Tipo                string `json:"tipo"`
	Videos              int    `json:"videos"`
	Factos              int    `json:"factos"`
	PresentesPelaRegra  int    `json:"presentes_pela_regra"`
	PresentesComAcentos int    `json:"presentes_com_acentos"`
	TempoMaximoMs       int64  `json:"tempo_maximo_ms"`
	CustoMicroUSD       *int64 `json:"custo_micro_usd_estimado,omitempty"`
}

// factoDoVideoNaSaida diz se o facto está na descrição, pela dobra dada: um de uma linha, se a
// linha ocorre; o de ordem, se as duas ocorrem e a primeira ocorrência de `antes` vem antes da
// primeira de `depois`.
func factoDoVideoNaSaida(saida string, f FactoDoVideo, dobra func(string) string) bool {
	s := dobra(saida)
	if f.Tipo != FactoOrdem {
		d := dobra(f.Valor)
		return d != "" && strings.Contains(s, d)
	}
	a, d := dobra(f.Antes), dobra(f.Depois)
	if a == "" || d == "" {
		return false
	}
	i, j := strings.Index(s, a), strings.Index(s, d)
	return i >= 0 && j >= 0 && i < j
}

// porTipoDoVideo devolve a contagem por tipo de um vídeo, só com os tipos que ele tem, pela ordem
// de [TiposDeFactoDoVideo], com os factos contados e nenhum presente.
func porTipoDoVideo(factos []FactoDoVideo) ([]ContagemDoTipo, map[string]int) {
	indice := map[string]int{}
	var out []ContagemDoTipo
	for _, t := range TiposDeFactoDoVideo {
		n := 0
		for _, f := range factos {
			if f.Tipo == t {
				n++
			}
		}
		if n > 0 {
			indice[t] = len(out)
			out = append(out, ContagemDoTipo{Tipo: t, Factos: n})
		}
	}
	return out, indice
}

// contarDescricao conta os factos de um vídeo numa descrição (vazia = nenhum presente), no total e
// por tipo, pela regra e com acentos.
func contarDescricao(l *LeituraDoVideo, texto string, factos []FactoDoVideo) {
	porTipo, indice := porTipoDoVideo(factos)
	for _, f := range factos {
		c := &porTipo[indice[f.Tipo]]
		pelaRegra := texto != "" && factoDoVideoNaSaida(texto, f, dobrarParaComparar)
		comAcentos := texto != "" && factoDoVideoNaSaida(texto, f, dobrarComAcentos)
		if pelaRegra {
			l.PresentesPelaRegra++
			c.PresentesPelaRegra++
		} else {
			l.AusentesPelaRegra = append(l.AusentesPelaRegra, f.Nome())
		}
		if comAcentos {
			l.PresentesComAcentos++
			c.PresentesComAcentos++
		} else if pelaRegra {
			l.SemOAcentoExacto = append(l.SemOAcentoExacto, f.Nome())
		}
	}
	l.PorTipo = porTipo
}

// limitesDaMedicaoDoVideo são ditos em todos os relatórios: os da bateria e os do motor.
func limitesDaMedicaoDoVideo(b *BateriaVideo, m MotorDeVideo) []string {
	return append([]string{
		fmt.Sprintf("videos sinteticos: %d videos, %d factos; nenhum video real", len(b.Videos), b.Factos()),
		"cenas estaticas desenhadas no banco: fundos de cor pura, formas geometricas cheias e texto na fonte bitmap 5x7 do OCR; nenhum movimento, camara, luz, som ou compressao de uma gravacao real",
		"os factos verificam-se pelo texto da resposta, pela regra do banco, e as contagens e as cores nas linhas que a instrucao pede (COUNT, BACKGROUND): um modelo que ve certo e escreve noutra forma perde esses factos",
		"o video com uma instrucao escrita no fotograma mede se o modelo a transcreve como dado; um que lhe obedece perde os factos dela",
		"o veredicto e contra o limiar de factos do dono (o do OCR, D7) sobre estes videos sinteticos: diz que o modelo descreve esta bateria, nao que descreve videos reais",
	}, m.Condicoes().Notas...)
}

// CorrerExperienciaVideo corre o motor sobre os vídeos dados (os da bateria, com ou sem MP4). Um
// vídeo que falha não pára a corrida: fica `falhou`, com a causa, e os factos dele contam como
// ausentes. Um contexto cancelado, ou um [ParagemDoVideo], pára entre dois vídeos e o relatório
// sai `interrompida`. `guardar`, quando presente, recebe a descrição de cada vídeo.
func CorrerExperienciaVideo(ctx context.Context, b *BateriaVideo, videos []VideoPronto, motor MotorDeVideo, relogio func() time.Time, guardar func(video, texto string)) *RelatorioVideo {
	if relogio == nil {
		relogio = time.Now
	}
	cond := motor.Condicoes()
	r := &RelatorioVideo{
		VersaoDoRelatorio: VersaoDoRelatorioVideo,
		Experiencia:       "video", DataUTC: relogio().UTC().Format(time.RFC3339),
		VersaoBateria: b.Versao, DigestBateria: b.Digest(),
		Motor: motor.Nome(), DescricaoMotor: motor.Descricao(),
		Limites:          LimitesDoVideo{TempoPorChamadaS: cond.TempoPorChamadaS, Rede: cond.Rede},
		LimitesDaMedicao: limitesDaMedicaoDoVideo(b, motor),
	}
	for _, vp := range videos {
		if ctx.Err() != nil {
			r.Interrompida = true
			break
		}
		v := vp.Video
		l := LeituraDoVideo{
			ID: v.ID, Largura: v.Largura, Altura: v.Altura, FPS: v.FPS, Segundos: v.Segundos(), Fotogramas: v.Fotogramas(),
			Cenas: len(v.Cenas), Bytes: len(vp.MP4), DigestDoMedia: vp.Digest, Factos: len(v.Factos),
		}
		inicio := relogio()
		resp, err := motor.Descrever(ctx, vp)
		l.TempoMs = relogio().Sub(inicio).Milliseconds()
		if resp.ComCusto {
			l.TokensDeEntrada, l.TokensDeSaida = resp.TokensDeEntrada, resp.TokensDeSaida
			if resp.CustoMicroUSD >= 0 {
				c := resp.CustoMicroUSD
				l.CustoMicroUSD = &c
			}
		}
		var paragem *ParagemDoVideo
		switch {
		case err != nil && ctx.Err() != nil && !errors.Is(err, ErrVideoTempo):
			r.Interrompida = true
		case errors.As(err, &paragem):
			r.Interrompida, r.Paragem = true, paragem.Causa
		case err != nil:
			l.Estado, l.Causa = EstadoVideoFalhou, causaDoErroDoVideo(err)
			contarDescricao(&l, "", v.Factos)
		default:
			l.Estado = EstadoVideoDescrito
			l.TamanhoDoTexto = len(resp.Texto)
			contarDescricao(&l, resp.Texto, v.Factos)
			if guardar != nil {
				guardar(v.ID, resp.Texto)
			}
		}
		if r.Interrompida {
			break
		}
		r.Videos = append(r.Videos, l)
	}
	r.PorTipo = resumoPorTipo(r.Videos)
	return r
}

func causaDoErroDoVideo(err error) string {
	switch {
	case errors.Is(err, ErrVideoTempo):
		return CausaVideoTempo
	case errors.Is(err, ErrVideoCortado):
		return CausaVideoCortado
	}
	return CausaVideoMotor
}

// resumoPorTipo agrega por tipo de facto, pela ordem de [TiposDeFactoDoVideo], e no total.
func resumoPorTipo(videos []LeituraDoVideo) []LinhaPorTipoDeFacto {
	var out []LinhaPorTipoDeFacto
	somarCusto := func(g *LinhaPorTipoDeFacto, c *int64) {
		if c == nil {
			return
		}
		soma := *c
		if g.CustoMicroUSD != nil {
			soma += *g.CustoMicroUSD
		}
		g.CustoMicroUSD = &soma
	}
	for _, t := range TiposDeFactoDoVideo {
		g := LinhaPorTipoDeFacto{Tipo: t}
		for _, l := range videos {
			for _, c := range l.PorTipo {
				if c.Tipo != t {
					continue
				}
				g.Videos++
				g.Factos += c.Factos
				g.PresentesPelaRegra += c.PresentesPelaRegra
				g.PresentesComAcentos += c.PresentesComAcentos
				g.TempoMaximoMs = max(g.TempoMaximoMs, l.TempoMs)
			}
		}
		if g.Factos > 0 {
			out = append(out, g)
		}
	}
	total := LinhaPorTipoDeFacto{Tipo: "total"}
	for _, l := range videos {
		total.Videos++
		total.Factos += l.Factos
		total.PresentesPelaRegra += l.PresentesPelaRegra
		total.PresentesComAcentos += l.PresentesComAcentos
		total.TempoMaximoMs = max(total.TempoMaximoMs, l.TempoMs)
		somarCusto(&total, l.CustoMicroUSD)
	}
	return append(out, total)
}

// VeredictoVideo calcula o veredicto de uma corrida `video` contra o limiar dado, pela
// [RegraDoVeredictoVideo]. Chama-se depois de o modo estar posto. `rota` é a rota medida (nil sem
// `--perfil`).
func VeredictoVideo(r *RelatorioVideo, limiar float64, rota *RotaMedidaDoOCR) *VeredictoDoVideo {
	v := &VeredictoDoVideo{Regra: RegraDoVeredictoVideo, LimiarDeFactos: limiar, Razoes: []string{}, RotaMedida: rota}
	for _, l := range r.Videos {
		v.VideosNasContas++
		if l.Estado != EstadoVideoDescrito {
			v.VideosQueFalharam++
		}
		v.Factos += l.Factos
		v.PresentesPelaRegra += l.PresentesPelaRegra
	}
	if r.Interrompida {
		v.Razoes = append(v.Razoes, "a corrida nao completou: o veredicto e sobre todos os videos da bateria")
	}
	if v.VideosNasContas == 0 || v.Factos == 0 {
		v.Razoes = append(v.Razoes, "nenhum video com factos entrou nas contas")
	}
	if v.VideosQueFalharam > 0 {
		v.Razoes = append(v.Razoes, fmt.Sprintf("%d video(s) nao foram descritos (tempo, erro do motor ou resposta cortada)", v.VideosQueFalharam))
	}
	if v.Factos > 0 && !factosNoLimiar(v.PresentesPelaRegra, v.Factos, limiar) {
		v.Razoes = append(v.Razoes, fmt.Sprintf("factos presentes pela regra: %d de %d, abaixo do limiar de %.1f%%", v.PresentesPelaRegra, v.Factos, 100*limiar))
	}
	v.Veredicto = VeredictoQualificado
	if len(v.Razoes) > 0 {
		v.Veredicto = VeredictoRecusado
	}
	switch {
	case v.Veredicto != VeredictoQualificado:
		v.PorqueNaoValeParaProducao = "o veredicto nao e qualificado"
	case r.Modo != ModoReal:
		v.PorqueNaoValeParaProducao = "a corrida nao foi do modo real: um motor falso nao e o modelo da tool"
	default:
		v.ValeParaProducao = true
	}
	return v
}

// ResumoVideoEmTexto é o resumo legível: o motor, uma tabela por vídeo, outra por tipo de facto,
// o veredicto e os limites declarados. Nunca leva a descrição do motor.
func ResumoVideoEmTexto(r *RelatorioVideo) string {
	var b strings.Builder
	fmt.Fprintf(&b, "experiencia video (AOS-537) — %s\n", r.DataUTC)
	fmt.Fprintf(&b, "bateria de video %s  digest %s\n", r.VersaoBateria, r.DigestBateria)
	fmt.Fprintf(&b, "motor %s  (%s)\n", r.Motor, r.DescricaoMotor)
	if r.Codificador != "" {
		fmt.Fprintf(&b, "MP4 gerados por %s\n", r.Codificador)
	}
	if r.Instrucao != nil {
		fmt.Fprintf(&b, "instrucao %s  %s  max_tokens %d\n", r.Instrucao.Versao, r.Instrucao.Digest, r.Instrucao.MaxTokens)
	}
	fmt.Fprintf(&b, "limites por chamada: %d s, rede %s\n", r.Limites.TempoPorChamadaS, r.Limites.Rede)
	if r.Pedidos != nil {
		fmt.Fprintf(&b, "pedidos ao fornecedor: %d (pior caso declarado %d, com a sonda)", r.Pedidos.Enviados, r.Pedidos.PiorCaso)
		if r.Custo != nil {
			fmt.Fprintf(&b, "; custo estimado da corrida %s", usd(r.Custo.MicroUSDDaCorrida))
		}
		b.WriteString("\n")
	}
	if r.Interrompida {
		b.WriteString("INTERROMPIDA: a corrida parou antes de descrever todos os videos")
		if r.Paragem != "" {
			fmt.Fprintf(&b, " (%s)", r.Paragem)
		}
		b.WriteString("\n")
	}
	b.WriteString("\npor video:\n")
	fmt.Fprintf(&b, "  %-24s %-8s %4s %5s %8s %-15s %7s %7s %8s %7s %8s %12s\n", "video", "dims", "fps", "s", "bytes", "estado", "regra", "acentos", "tempo", "texto", "tokens", "custo")
	for _, l := range r.Videos {
		estado := l.Estado
		if l.Causa != "" {
			estado += ":" + l.Causa
		}
		fmt.Fprintf(&b, "  %-24s %-8s %4d %5d %8d %-15s %3d/%-3d %3d/%-3d %6dms %6dB %8d %12s\n", l.ID, fmt.Sprintf("%dx%d", l.Largura, l.Altura), l.FPS, l.Segundos, l.Bytes, estado,
			l.PresentesPelaRegra, l.Factos, l.PresentesComAcentos, l.Factos, l.TempoMs, l.TamanhoDoTexto, l.TokensDeEntrada+l.TokensDeSaida, usd(l.CustoMicroUSD))
	}
	b.WriteString("\npor tipo de facto (regra = sem acentos nem maiusculas, como o banco):\n")
	fmt.Fprintf(&b, "  %-10s %6s %9s %7s %7s %8s %12s\n", "tipo", "videos", "factos", "regra", "acentos", "t. max", "custo")
	for _, g := range r.PorTipo {
		fmt.Fprintf(&b, "  %-10s %6d %9d %7s %7s %6dms %12s\n", g.Tipo, g.Videos, g.Factos,
			percentagem(g.PresentesPelaRegra, g.Factos), percentagem(g.PresentesComAcentos, g.Factos), g.TempoMaximoMs, usd(g.CustoMicroUSD))
	}
	if v := r.VeredictoDoVideo; v != nil {
		fmt.Fprintf(&b, "\nVEREDICTO: %s — limiar do dono: pelo menos %.1f%% dos factos presentes pela regra; %d de %d em %d video(s), %d nao descrito(s)\n",
			strings.ToUpper(v.Veredicto), 100*v.LimiarDeFactos, v.PresentesPelaRegra, v.Factos, v.VideosNasContas, v.VideosQueFalharam)
		for _, razao := range v.Razoes {
			fmt.Fprintf(&b, "  - %s\n", razao)
		}
		if v.ValeParaProducao {
			b.WriteString("  e a medida de um modelo (modo real)\n")
		} else {
			fmt.Fprintf(&b, "  NAO vale para producao: %s\n", v.PorqueNaoValeParaProducao)
		}
		if rm := v.RotaMedida; rm != nil {
			imagem := "sem proxy"
			if rm.DigestDaImagemDoProxy != "" {
				imagem = rm.DigestDaImagemDoProxy
			}
			fmt.Fprintf(&b, "  rota medida: fornecedor=%s modelo=%s esperado=%s\n  perfil=%s\n  imagem do proxy=%s\n", rm.Fornecedor, rm.Modelo, rm.ModeloEsperado, rm.DigestDoPerfil, imagem)
		}
	}
	if r.DigestDoRelatorio != "" {
		fmt.Fprintf(&b, "digest do relatorio: %s\n", r.DigestDoRelatorio)
	}
	b.WriteString("\nlimites desta medicao:\n")
	for _, l := range r.LimitesDaMedicao {
		fmt.Fprintf(&b, "  - %s\n", l)
	}
	return b.String()
}

// EscreverRelatorioVideo grava o relatório e o resumo na pasta e devolve os dois caminhos. O nome
// é `ensaio-video-<motor>-<data>`; num motor que é um modelo pago, `ensaio-real-video-<motor>-<data>`,
// para entrar na reconstrução do contador do dia como as outras corridas reais. Nunca escreve por
// cima de outro relatório.
func EscreverRelatorioVideo(pasta string, r *RelatorioVideo) (string, string, error) {
	if err := os.MkdirAll(pasta, 0o700); err != nil {
		return "", "", fmt.Errorf("banco-ensaio: pasta dos relatorios: %w", err)
	}
	carimbo := strings.NewReplacer("-", "", ":", "").Replace(r.DataUTC)
	prefixo := "ensaio-video-"
	if r.Modo == ModoReal {
		prefixo = "ensaio-" + ModoReal + "-video-"
	}
	base := filepath.Join(pasta, prefixo+r.Motor+"-"+carimbo)
	r.DigestDoRelatorio = ""
	semDigest, err := json.Marshal(r)
	if err != nil {
		return "", "", err
	}
	if r.DigestDoRelatorio, err = DigestDoRelatorio(semDigest); err != nil {
		return "", "", err
	}
	cru, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return "", "", err
	}
	f, err := os.OpenFile(base+".json", os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600) // #nosec G304 -- pasta dos relatorios, dada por quem corre
	if err != nil {
		return "", "", fmt.Errorf("banco-ensaio: ja existe um relatorio de video com este nome (mesmo segundo, mesma pasta); nao se escreve por cima: %s", filepath.Base(base)+".json")
	}
	_, werr := f.Write(append(cru, '\n'))
	if ferr := f.Close(); werr == nil {
		werr = ferr
	}
	if werr != nil {
		return "", "", werr
	}
	if err := os.WriteFile(base+".txt", []byte(ResumoVideoEmTexto(r)), 0o600); err != nil {
		return "", "", err
	}
	return base + ".json", base + ".txt", nil
}

// FormatoDasDescricoesGuardadas é o formato de cada ficheiro das descrições guardadas.
const FormatoDasDescricoesGuardadas = "aos-ensaio/descricao-video/v1"

// EscreverDescricoesGuardadas grava, ao lado do relatório, a pasta `respostas-<nome do
// relatório>/` com um ficheiro JSON por vídeo: a descrição do motor. Recusa escrever numa pasta que
// já exista.
func EscreverDescricoesGuardadas(relatorioEmJSON, motor string, textos map[string]string) (string, error) {
	return escreverTextosGuardados(relatorioEmJSON, motor, FormatoDasDescricoesGuardadas, "video", textos)
}
