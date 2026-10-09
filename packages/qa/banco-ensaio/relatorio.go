package bancoensaio

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	modelgateway "github.com/aos-ref/platform/model-gateway"
)

// O RELATÓRIO. Só contagens, fichas em vocabulário fechado, identificadores de caso, os digests
// da bateria, da rota e da configuração, e o nome do modelo. NÃO leva: chaves, cabeçalhos, o
// endereço da rota, nem um byte de texto de pedidos, de respostas ou de raciocínio.

// VersaoDoRelatorio é a versão do formato do relatório.
const VersaoDoRelatorio = "1"

// RotaDoRelatorio identifica a rota ensaiada.
type RotaDoRelatorio struct {
	Fornecedor string `json:"fornecedor"`
	// Modelo é o nome do modelo no fornecedor (nos modos sem modelo real, o do provider falso).
	Modelo string `json:"modelo"`
	// Digest é o sha256 do fornecedor, do modelo e do endereço da rota. O endereço não vai.
	Digest string `json:"digest"`
}

// DigestDaRota calcula o digest de uma rota. O endereço entra no digest e em mais lado nenhum.
func DigestDaRota(fornecedor, modelo, endereco string) string {
	soma := sha256.Sum256([]byte(fornecedor + "\n" + modelo + "\n" + endereco))
	return "sha256:" + hex.EncodeToString(soma[:])
}

// Relatorio é o relatório de uma corrida.
type Relatorio struct {
	Versao      string `json:"versao_do_relatorio"`
	Ticket      string `json:"ticket"`
	DataUTC     string `json:"data_utc"`
	Modo        string `json:"modo"`
	Experiencia string `json:"experiencia"`
	// Terminou diz como a corrida acabou: completa, ou a causa de ter parado a meio.
	Terminou string          `json:"terminou"`
	Rota     RotaDoRelatorio `json:"rota"`
	// RegiaoDeclarada é uma DECLARAÇÃO do dono, copiada do ficheiro de chaves. Não tem efeito
	// no ensaio e o banco não a verifica.
	RegiaoDeclarada *Declaracao `json:"regiao_de_processamento_declarada,omitempty"`
	Protocolo       Protocolo   `json:"protocolo"`
	Digests         Digests     `json:"digests"`
	Plano           Plano       `json:"plano"`
	Pedidos         Pedidos     `json:"pedidos"`
	Custo           Custo       `json:"custo"`
	// Sonda é o pedido de sonda feito antes da corrida (só quando houve). Uma sonda sem 200
	// significa que nenhum caso correu.
	Sonda *Sonda `json:"sonda,omitempty"`
	// Taxas de todas as observações da corrida.
	Taxas Taxas `json:"taxas"`
	// PorBraco: as mesmas taxas, por braço.
	PorBraco []TaxasDoBraco `json:"por_braco"`
	// PorCaso: as mesmas taxas, por caso.
	PorCaso []TaxasDoCaso `json:"por_caso"`
	// Comparacoes dos braços com o braço A (só na experiência dos separadores).
	Comparacoes []Comparacao `json:"comparacoes,omitempty"`
	// Limites são as frases do que esta corrida NÃO prova. São fixas: não dependem dos números.
	Limites []string `json:"limites"`
	// Observacoes: uma linha por run, em vocabulário fechado.
	Observacoes []Observacao `json:"observacoes"`
	// FormaNoFornecedor é a forma com que os turnos com tool calls chegaram ao provider FALSO
	// (AOS-516): nomes de chaves e tipos de bloco, nunca valores. Só nos modos sem modelo real.
	FormaNoFornecedor *FormaNoFornecedor `json:"forma_no_fornecedor,omitempty"`
	// Qualificacao é o VEREDICTO do banco sobre a devolução do estado opaco (AOS-516). Existe
	// quando o perfil da corrida devolve estado, ou quando a corrida foi feita contra o provider
	// falso do estado. É este campo que se lê; as contagens explicam-no.
	Qualificacao *QualificacaoDaDevolucao `json:"qualificacao_da_devolucao,omitempty"`
}

// Declaracao é um valor declarado por alguém e não verificado pelo banco.
type Declaracao struct {
	Valor string `json:"valor"`
	Nota  string `json:"nota"`
}

// Protocolo diz em que forma os pedidos foram enviados.
type Protocolo struct {
	Projeccao string `json:"projeccao"`
	Layout    string `json:"layout_do_prompt"`
	// VersaoPorBraco é a versão PUBLICADA da projecção nativa de que cada braço parte.
	VersaoPorBraco map[string]string `json:"versao_publicada_por_braco"`
	// Estado é o que o nó de ensaio ligou por o perfil candidato devolver estado (AOS-516):
	// nesse caso a versão da projecção é a do perfil, e não a do braço. Ausente sem devolução.
	Estado *ComposicaoDoEstado `json:"estado_opaco,omitempty"`
}

// Digests são os três digests que fixam o que foi medido.
type Digests struct {
	Bateria      string `json:"bateria"`
	Rota         string `json:"rota"`
	Configuracao string `json:"configuracao"`
	// Perfil é o digest do perfil de rota candidato com que a corrida foi feita (AOS-513). É a
	// referência que o runbook da rota exige antes de o perfil ser assinado para produção.
	// Ausente quando a corrida não levou perfil.
	Perfil string `json:"perfil,omitempty"`
}

// Pedidos é a contabilidade de pedidos da corrida e do dia.
type Pedidos struct {
	// PrevistosMax é o máximo que o plano podia fazer; Enviados, os que saíram nesta corrida.
	PrevistosMax int64 `json:"previstos_max"`
	Enviados     int64 `json:"enviados"`
	// Os campos do tecto são nil quando a corrida não tinha contador (modos sem modelo real).
	TectoDoDia    *int64 `json:"tecto_do_dia"`
	GastosHoje    *int64 `json:"gastos_hoje"`
	RestantesHoje *int64 `json:"restantes_hoje"`
	DiaUTC        string `json:"dia_utc,omitempty"`
}

// Custo é o gasto ESTIMADO da corrida.
type Custo struct {
	// Estimativa é sempre true: o gasto sai dos tokens do `usage` e de uma tabela de preços
	// declarada, não da factura do fornecedor.
	Estimativa      bool   `json:"estimativa"`
	Nota            string `json:"nota"`
	TokensDeEntrada int64  `json:"tokens_de_entrada"`
	TokensDeSaida   int64  `json:"tokens_de_saida"`
	// Os campos em micro-USD são nil quando o dono não definiu um tecto em dólares.
	MicroUSDHoje     *int64 `json:"micro_usd_estimado_hoje"`
	TectoMicroUSD    *int64 `json:"tecto_micro_usd_do_dia"`
	MicroUSDRestante *int64 `json:"micro_usd_restante_hoje"`
}

// TaxasDoBraco são as taxas de um braço.
type TaxasDoBraco struct {
	Braco           string `json:"braco"`
	Descricao       string `json:"descricao"`
	VersaoPublicada string `json:"versao_publicada"`
	Taxas           Taxas  `json:"taxas"`
}

// TaxasDoCaso são as taxas de um caso.
type TaxasDoCaso struct {
	Caso  string `json:"caso"`
	Taxas Taxas  `json:"taxas"`
}

// Comparacao compara a taxa de primeiras tentativas sem tool call de um braço com a do braço A.
// É aritmética sobre as contagens; não é uma conclusão.
type Comparacao struct {
	Braco      string `json:"braco"`
	ContraOQue string `json:"contra"`
	Mede       string `json:"mede"`
	// Diferenca é a taxa do braço menos a do braço A; nil se algum não tem denominador.
	Diferenca *float64 `json:"diferenca_de_taxas"`
	// IntervalosSobrepostos diz se os intervalos de 95% dos dois braços se tocam.
	IntervalosSobrepostos *bool `json:"intervalos_de_95_sobrepostos"`
	// PValor é o do teste EXACTO de Fisher, bilateral. Não é uma aproximação normal: com
	// contagens baixas num dos braços — o cenário esperado se uma variante funcionar — a
	// aproximação declara diferença onde o teste exacto não declara (0/53 contra 4/53: 0,041
	// pela aproximação, 0,118 pelo exacto).
	PValor *float64 `json:"p_valor_fisher_exacto"`
	// PValorCorrigido é o p-valor corrigido pelo método de Holm para o conjunto das comparações
	// com o braço A (três, na experiência dos separadores).
	PValorCorrigido *float64 `json:"p_valor_corrigido_holm"`
	// DistingueA5 diz se o p-valor CORRIGIDO fica abaixo de 0,05. É o que a amostra deixa
	// afirmar com as três comparações feitas ao mesmo tempo.
	DistingueA5 *bool `json:"distingue_a_5_por_cento"`
}

// limitesDaCorrida devolve as frases do que a corrida não prova.
func limitesDaCorrida(cfg CfgDaCorrida) []string {
	l := []string{
		"O banco devolve taxas; nao aceita nem recusa um modelo.",
		"A taxa de tool call em texto e medida sem ler texto: conta os runs que exigiam uma tool, nao pediram nenhuma tool call nativa e pararam com o motivo stop. O nome da tool no texto e uma heuristica de contagem, nao uma deteccao.",
		"A recusa do proprio objectivo e medida por um substituto: os factos do documento sintetico estao ou nao na saida. Uma saida sem os factos nao e, so por isso, uma recusa.",
		"O no de ensaio e minimo: sem PDP, WORM, sandbox nem aos-orq. Mede a fronteira com o modelo, nao a governacao do no.",
	}
	switch cfg.Modo {
	case ModoFalso:
		l = append(l, "Modo falso: o fornecedor e um provider falso com um roteiro fixo. Nada aqui diz o que um modelo real faz.")
	case ModoProxy:
		l = append(l, "Modo proxy: a imagem real do proxy a frente de um provider falso. Mede o que o proxy faz ao pedido e a resposta; nada aqui diz o que um modelo real faz.")
	case ModoReal:
		l = append(l, "Uma corrida, uma rota, um dia: as taxas sao desta corrida e nao de producao (outro prompt de sistema, outras tools, outros documentos).")
	}
	if cfg.Modo == ModoProxy || cfg.Modo == ModoReal {
		l = append(l,
			"O proxy do ensaio nao tem a configuracao de producao: corre com num_retries: 0 (um pedido do banco e um pedido ao fornecedor; producao deixa o proxy repetir), drop_params: true e disable_cooldowns: true (o proxy nao poe a rota em arrefecimento depois de um erro).",
			"Os 429 contam-se a parte dos erros do provider: tanto os da o fornecedor como o proprio proxy, e pelo codigo HTTP nao se distinguem. Uma serie de 429 a seguir a outro erro e sinal de arrefecimento do proxy, nao do fornecedor.",
		)
	}
	if cfg.Plano.Experiencia == ExperienciaSeparadores {
		l = append(l,
			fmt.Sprintf("Com %d amostras por braco so se distingue uma taxa de 10%% de uma de 32%% (potencia de 80%% a 5%%). Diferencas menores nao se veem com esta amostra: nao as ver nao prova que nao existem.", cfg.Plano.Amostras),
			"Intervalos de 95% que se sobrepoem nao mostram diferenca entre bracos.",
			"O p-valor de cada braco contra o A e o do teste exacto de Fisher (bilateral). Sao tres comparacoes com o mesmo braco A: o p-valor corrigido (Holm) e o que decide o campo distingue_a_5_por_cento, e so esse controla a 5% o erro do conjunto.",
			"A potencia declarada vale para uma base perto de 10%: com uma base de 40 a 50% a diferenca que se ve e de cerca de 27 pontos.",
			"A experiencia corre sobre um so caso (T1) e um turno por amostra: nao mede o segundo turno, a resposta vazia nem a recuperacao.",
			"As variantes B e C existem so no banco. Um braco com menos falhas nao e uma versao de projeccao: publicar uma exige ticket proprio e emenda ao ADR-036.",
		)
	}
	if cfg.No != nil && cfg.No.devolucao != nil {
		l = append(l,
			"Devolucao do estado opaco: so conta como devolvido o pedido em que o gateway armou o estado de todos os turnos E a que o fornecedor respondeu 2xx. A decisao do gateway toma-se antes do envio e, sozinha, nao conta. O banco nao le o estado, e nao prova que o fornecedor o validou: um provider falso nao verifica assinaturas, e so o modelo real o faz.",
			"Um turno com raciocinio e um turno cuja resposta trouxe pelo menos um campo de raciocinio ou de assinatura, pelos nomes dos campos que a sonda do gateway leu; o banco nao le os valores, e um campo presente com valor vazio conta. Um envelope so com os ids das tool calls conta a parte e nao entra na taxa.",
			"A governacao da rota corre em observe: compara o modelo que o proxy DECLARA ter servido com o do perfil. Nao e atestacao. No modo falso nao ha proxy: quem declara o modelo servido e o proprio provider falso, com o nome que o perfil espera.",
			"Com devolver em obrigatorio, um turno cujo estado nao se pode devolver para o run (estado_nao_devolvido): o pedido seguinte nao e enviado. Esses runs contam em recusas_por_falta_de_estado, nao em erros do provider.",
		)
	}
	return l
}

func construirRelatorio(cfg CfgDaCorrida, agora time.Time, previstos int64, terminou string, sonda *Sonda, obs []Observacao) (*Relatorio, error) {
	var estado *ComposicaoDoEstado
	layout := agentruntime.AssemblyVersion140
	if cfg.No != nil && cfg.No.devolucao != nil {
		estado, layout = cfg.No.devolucao, cfg.No.devolucao.Layout
	}
	r := &Relatorio{
		Versao: VersaoDoRelatorio, Ticket: "AOS-512", DataUTC: agora.Format(time.RFC3339),
		Modo: cfg.Modo, Experiencia: string(cfg.Plano.Experiencia), Terminou: terminou,
		Rota: cfg.Rota,
		Protocolo: Protocolo{
			Projeccao: modelgateway.ProjectionNative, Layout: layout,
			VersaoPorBraco: map[string]string{}, Estado: estado,
		},
		Digests: Digests{
			Bateria: cfg.Bateria.Digest(), Rota: cfg.Rota.Digest,
			Configuracao: digestDaConfiguracao(cfg.Plano, cfg.Extra, estado),
			Perfil:       cfg.PerfilDigest,
		},
		Plano:       cfg.Plano,
		Taxas:       CalcularTaxas(obs),
		Limites:     limitesDaCorrida(cfg),
		Observacoes: obs,
		Custo: Custo{
			Estimativa: true,
			Nota:       "gasto estimado pelos tokens do usage e pela tabela de precos declarada; nao e a factura do fornecedor",
		},
		Pedidos: Pedidos{PrevistosMax: previstos},
		Sonda:   sonda,
	}
	if r.Observacoes == nil {
		r.Observacoes = []Observacao{}
	}
	if estado != nil || cfg.QualificaDevolucao {
		r.Qualificacao = qualificarDevolucao(estado != nil, terminou == TerminouCompleta, r.Taxas.Devolucao, obs)
	}
	if cfg.RegiaoDeclarada != "" {
		r.RegiaoDeclarada = &Declaracao{Valor: cfg.RegiaoDeclarada, Nota: "declarada pelo dono no ficheiro de chaves; sem efeito no ensaio e nao verificada"}
	}
	if sonda != nil {
		// A sonda é um pedido enviado e pago como os outros; não é uma observação.
		if sonda.enviada {
			r.Pedidos.Enviados++
		}
		r.Custo.TokensDeEntrada += sonda.tokensDeEntrada
		r.Custo.TokensDeSaida += sonda.tokensDeSaida
	}
	for _, o := range obs {
		r.Pedidos.Enviados += int64(o.Pedidos)
		r.Custo.TokensDeEntrada += o.TokensDeEntrada
		r.Custo.TokensDeSaida += o.TokensDeSaida
	}
	if cfg.Contador != nil {
		e, err := cfg.Contador.Estado()
		if err != nil {
			return nil, err
		}
		// Com contador, «enviados» são as reservas feitas: conta também um pedido reservado que
		// não chegou a ter resposta.
		r.Pedidos.Enviados = e.EnviadosNestaCorrida
		r.Pedidos.TectoDoDia, r.Pedidos.GastosHoje, r.Pedidos.RestantesHoje = &e.TectoPedidos, &e.PedidosHoje, &e.PedidosRestantes
		r.Pedidos.DiaUTC = e.Dia
		if e.TemUSD {
			r.Custo.MicroUSDHoje, r.Custo.TectoMicroUSD, r.Custo.MicroUSDRestante = &e.MicroUSDHoje, &e.TectoMicroUSD, &e.MicroUSDRestante
		}
	}
	porBraco := map[string][]Observacao{}
	porCaso := map[string][]Observacao{}
	for _, o := range obs {
		porBraco[o.Braco] = append(porBraco[o.Braco], o)
		porCaso[o.Caso] = append(porCaso[o.Caso], o)
	}
	for _, b := range cfg.Plano.Bracos {
		r.Protocolo.VersaoPorBraco[string(b)] = VersaoPublicadaDoBraco(b)
		r.PorBraco = append(r.PorBraco, TaxasDoBraco{
			Braco: string(b), Descricao: DescricaoDoBraco(b), VersaoPublicada: VersaoPublicadaDoBraco(b),
			Taxas: CalcularTaxas(porBraco[string(b)]),
		})
	}
	for _, c := range cfg.Plano.Casos {
		r.PorCaso = append(r.PorCaso, TaxasDoCaso{Caso: c, Taxas: CalcularTaxas(porCaso[c])})
	}
	if cfg.Plano.Experiencia == ExperienciaSeparadores {
		r.Comparacoes = compararComA(r.PorBraco)
	}
	return r, nil
}

// compararComA compara cada braço com o braço A na taxa de primeiras tentativas sem tool call.
func compararComA(bracos []TaxasDoBraco) []Comparacao {
	var a *Taxa
	for i := range bracos {
		if bracos[i].Braco == string(BracoA) {
			a = &bracos[i].Taxas.SemToolCallNaPrimeira
		}
	}
	if a == nil {
		return nil
	}
	mede := map[string]string{
		string(BracoB): "os sinais de menor e maior nos separadores",
		string(BracoC): "a linha de fim",
		string(BracoD): "a projeccao 1.2.0 contra a linha de base 1.0.0",
	}
	var out []Comparacao
	for i := range bracos {
		if bracos[i].Braco == string(BracoA) {
			continue
		}
		t := bracos[i].Taxas.SemToolCallNaPrimeira
		c := Comparacao{Braco: bracos[i].Braco, ContraOQue: string(BracoA), Mede: mede[bracos[i].Braco]}
		if t.Valor != nil && a.Valor != nil {
			d := *t.Valor - *a.Valor
			sobrepostos := !(*t.IC95Inferior > *a.IC95Superior || *a.IC95Inferior > *t.IC95Superior)
			p := pValorDeFisher(t.Numerador, t.Denominador, a.Numerador, a.Denominador)
			c.Diferenca, c.IntervalosSobrepostos, c.PValor = &d, &sobrepostos, &p
		}
		out = append(out, c)
	}
	// A correcção de Holm sobre as comparações que têm p-valor.
	var ps []float64
	var onde []int
	for i := range out {
		if out[i].PValor != nil {
			ps = append(ps, *out[i].PValor)
			onde = append(onde, i)
		}
	}
	for k, corrigido := range corrigirPorHolm(ps) {
		corrigido := corrigido
		distingue := corrigido < 0.05
		out[onde[k]].PValorCorrigido, out[onde[k]].DistingueA5 = &corrigido, &distingue
	}
	return out
}

// pValorDeFisher é o p-valor bilateral do teste exacto de Fisher para a tabela 2×2 de dois
// braços (x1 em n1 contra x2 em n2): a soma das probabilidades hipergeométricas de todas as
// tabelas com as mesmas margens que são tão ou menos prováveis do que a observada. Calcula-se
// em logaritmos, para não transbordar.
func pValorDeFisher(x1, n1, x2, n2 int) float64 {
	if n1 <= 0 || n2 <= 0 || x1 < 0 || x2 < 0 || x1 > n1 || x2 > n2 {
		return 1
	}
	k := x1 + x2
	logComb := func(n, r int) float64 {
		a, _ := math.Lgamma(float64(n + 1))
		b, _ := math.Lgamma(float64(r + 1))
		c, _ := math.Lgamma(float64(n - r + 1))
		return a - b - c
	}
	total := logComb(n1+n2, k)
	prob := func(a int) float64 { return math.Exp(logComb(n1, a) + logComb(n2, k-a) - total) }
	observada := prob(x1)
	soma := 0.0
	for a := max(0, k-n2); a <= min(k, n1); a++ {
		// A tolerância relativa segura as tabelas simétricas, que têm a mesma probabilidade a
		// menos do arredondamento.
		if p := prob(a); p <= observada*(1+1e-7) {
			soma += p
		}
	}
	return math.Min(1, soma)
}

// corrigirPorHolm devolve os p-valores corrigidos pelo método de Holm, na ordem de entrada: o
// menor multiplica-se pelo número de comparações, o seguinte por menos uma, e assim por diante,
// sem nunca descer abaixo do corrigido anterior nem passar de 1.
func corrigirPorHolm(ps []float64) []float64 {
	m := len(ps)
	ordem := make([]int, m)
	for i := range ordem {
		ordem[i] = i
	}
	sort.SliceStable(ordem, func(a, b int) bool { return ps[ordem[a]] < ps[ordem[b]] })
	out := make([]float64, m)
	anterior := 0.0
	for posicao, i := range ordem {
		v := math.Min(1, ps[i]*float64(m-posicao))
		if v < anterior {
			v = anterior
		}
		out[i], anterior = v, v
	}
	return out
}

// EscreverRelatorio grava o relatório em dois ficheiros na pasta dada — `<base>.json` e
// `<base>.txt` — e devolve os dois caminhos. O nome dos ficheiros leva o modo, a experiência e
// a data; nunca um valor do ficheiro de chaves.
func EscreverRelatorio(pasta string, r *Relatorio) (string, string, error) {
	if err := os.MkdirAll(pasta, 0o700); err != nil {
		return "", "", fmt.Errorf("banco-ensaio: pasta dos relatorios: %w", err)
	}
	carimbo := strings.NewReplacer("-", "", ":", "").Replace(r.DataUTC)
	base := filepath.Join(pasta, fmt.Sprintf("ensaio-%s-%s-%s", r.Modo, r.Experiencia, carimbo))
	cru, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return "", "", err
	}
	if err := os.WriteFile(base+".json", append(cru, '\n'), 0o600); err != nil {
		return "", "", err
	}
	if err := os.WriteFile(base+".txt", []byte(ResumoEmTexto(r)), 0o600); err != nil {
		return "", "", err
	}
	return base + ".json", base + ".txt", nil
}

func linhaDaTaxa(nome string, t Taxa) string {
	if t.Valor == nil {
		return fmt.Sprintf("  %-40s %d/%d  (sem denominador)\n", nome, t.Numerador, t.Denominador)
	}
	return fmt.Sprintf("  %-40s %d/%d = %5.1f%%  IC95 [%5.1f%%, %5.1f%%]\n", nome, t.Numerador, t.Denominador,
		100**t.Valor, 100**t.IC95Inferior, 100**t.IC95Superior)
}

func blocoDeTaxas(b *strings.Builder, t Taxas) {
	b.WriteString(linhaDaTaxa("sem tool call na 1.a tentativa", t.SemToolCallNaPrimeira))
	b.WriteString(linhaDaTaxa("  das quais, nome da tool no texto", t.NomeDaToolNoTexto))
	b.WriteString(linhaDaTaxa("2.o turno com tools aceite", t.SegundoTurnoAceite))
	b.WriteString(linhaDaTaxa("factos ausentes da saida", t.FactosAusentes))
	b.WriteString(linhaDaTaxa("resposta vazia (empty_output)", t.RespostaVazia))
	b.WriteString(linhaDaTaxa("cortada (truncated)", t.Cortada))
	b.WriteString(linhaDaTaxa("erro do provider (HTTP != 200, 429)", t.ErroDoProvider))
	b.WriteString(linhaDaTaxa("limite de taxa (HTTP 429)", t.LimiteDeTaxa429))
	b.WriteString(linhaDaTaxa("cumprido a 1.a tentativa", t.CumpridoAPrimeira))
	b.WriteString(linhaDaTaxa("recuperado a 2.a tentativa", t.RecuperadoASegunda))
	b.WriteString(linhaDaTaxa("recuperado a 3.a tentativa", t.RecuperadoATerceira))
}

// blocoDaDevolucao escreve as contagens da devolução do estado opaco (AOS-516).
func blocoDaDevolucao(b *strings.Builder, d *Devolucao) {
	if d == nil {
		return
	}
	fmt.Fprintf(b, "  DEVOLUCAO DO ESTADO OPACO: %d turno(s) com estado capturado, dos quais %d com raciocinio ou assinatura e %d so com ids; %d pedido(s) com turnos anteriores\n",
		d.TurnosComEstadoCapturado, d.TurnosComRaciocinioCapturado, d.TurnosSoComIDs, d.PedidosComTurnosAnteriores)
	b.WriteString(linhaDaTaxa("  aceites pelo fornecedor (P4)", d.TaxaDeDevolucao))
	fmt.Fprintf(b, "    decididos a devolver pelo gateway (ANTES do envio; nao e resultado): %d;  recusas por falta de estado (pedido NAO enviado): %d\n",
		d.DecididosADevolver, d.Recusas)
	c := d.ComEstado
	fmt.Fprintf(b, "    pedidos com estado: %d — tentativas: 2xx %d, 4xx (sem 429) %d, 429 %d, 5xx %d, outro %d, erro de transporte %d, nao enviados %d\n",
		c.Pedidos, c.HTTP2xx, c.HTTP4xx, c.HTTP429, c.HTTP5xx, c.HTTPOutro, c.ErroDeTransporte, c.NaoEnviado)
	blocoDeContagens(b, "  capturas (por turno com estado)", d.CapturasPorResultado)
	blocoDeContagens(b, "  pedidos por decisao do gateway", d.PedidosPorDecisao)
	blocoDeContagens(b, "  nao devolvido, por causa", d.NaoDevolvidoPorCausa)
}

func blocoDeContagens(b *strings.Builder, titulo string, m map[string]int) {
	b.WriteString("  " + titulo + ":")
	if len(m) == 0 {
		b.WriteString(" (nenhum)\n")
		return
	}
	b.WriteString("\n")
	for _, k := range chavesOrdenadas(m) {
		fmt.Fprintf(b, "    %-90s %d\n", k, m[k])
	}
}

// ResumoEmTexto devolve o resumo do relatório em texto simples.
func ResumoEmTexto(r *Relatorio) string {
	var b strings.Builder
	fmt.Fprintf(&b, "BANCO DE ENSAIO (AOS-512) - relatorio de taxas\n")
	fmt.Fprintf(&b, "data (UTC): %s   modo: %s   experiencia: %s   terminou: %s\n", r.DataUTC, r.Modo, r.Experiencia, r.Terminou)
	fmt.Fprintf(&b, "rota: fornecedor=%s modelo=%s\n", r.Rota.Fornecedor, r.Rota.Modelo)
	if r.RegiaoDeclarada != nil {
		fmt.Fprintf(&b, "regiao de processamento DECLARADA pelo dono (sem efeito no ensaio): %s\n", r.RegiaoDeclarada.Valor)
	}
	fmt.Fprintf(&b, "protocolo: projeccao %s, layout %s\n", r.Protocolo.Projeccao, r.Protocolo.Layout)
	if e := r.Protocolo.Estado; e != nil {
		fmt.Fprintf(&b, "estado opaco: captura %s, governacao da rota %s (endpoint comparado: %v), projeccao do perfil %s, devolver %s, tool_call_id %s\n",
			e.Captura, e.GovernacaoDaRota, e.EndpointComparado, e.Projeccao, e.Devolver, e.ToolCallID)
	}
	fmt.Fprintf(&b, "digests: bateria=%s\n         rota=%s\n         configuracao=%s\n", r.Digests.Bateria, r.Digests.Rota, r.Digests.Configuracao)
	if r.Digests.Perfil != "" {
		fmt.Fprintf(&b, "         perfil candidato=%s\n", r.Digests.Perfil)
	}
	fmt.Fprintf(&b, "plano: %d amostras por braco, semente %d, ate %d tentativas, ate %d turnos, casos %s\n",
		r.Plano.Amostras, r.Plano.Semente, r.Plano.Tentativas, r.Plano.MaxTurnos, strings.Join(r.Plano.Casos, ","))
	fmt.Fprintf(&b, "pedidos: previstos (maximo) %d, enviados %d", r.Pedidos.PrevistosMax, r.Pedidos.Enviados)
	if r.Pedidos.TectoDoDia != nil {
		fmt.Fprintf(&b, "; dia %s: gastos %d de %d, restam %d", r.Pedidos.DiaUTC, *r.Pedidos.GastosHoje, *r.Pedidos.TectoDoDia, *r.Pedidos.RestantesHoje)
	} else {
		b.WriteString("; sem contador do dia nesta corrida")
	}
	b.WriteString("\n")
	fmt.Fprintf(&b, "tokens: entrada %d, saida %d", r.Custo.TokensDeEntrada, r.Custo.TokensDeSaida)
	if r.Custo.MicroUSDHoje != nil {
		fmt.Fprintf(&b, "; gasto ESTIMADO hoje %.4f USD de %.2f USD, restam %.4f USD",
			float64(*r.Custo.MicroUSDHoje)/1e6, float64(*r.Custo.TectoMicroUSD)/1e6, float64(*r.Custo.MicroUSDRestante)/1e6)
	}
	b.WriteString("\n")
	if r.Sonda != nil {
		fmt.Fprintf(&b, "sonda antes da corrida (1 pedido, conta no tecto): HTTP %d, resultado %s\n", r.Sonda.HTTP, r.Sonda.Resultado)
	}
	b.WriteString("\n")

	fmt.Fprintf(&b, "TODAS AS OBSERVACOES (%d runs, %d unidades)\n", r.Taxas.N, r.Taxas.Unidades)
	blocoDeTaxas(&b, r.Taxas)
	blocoDeContagens(&b, "desfechos", r.Taxas.Desfechos)
	blocoDeContagens(&b, "motivos de paragem (por turno)", r.Taxas.MotivosDeParagem)
	blocoDeContagens(&b, "codigos HTTP (por pedido)", r.Taxas.HTTP)
	blocoDeContagens(&b, "tipos de erro (por pedido com resposta sem 200; vocabulario fechado)", r.Taxas.TiposDeErro)
	blocoDeContagens(&b, "fichas da forma da resposta (por pedido)", r.Taxas.Fichas)
	blocoDaDevolucao(&b, r.Taxas.Devolucao)
	if q := r.Qualificacao; q != nil {
		razoes := "(nenhuma)"
		if len(q.Razoes) > 0 {
			razoes = strings.Join(q.Razoes, ", ")
		}
		fmt.Fprintf(&b, "  QUALIFICACAO DA DEVOLUCAO: %s — razoes: %s\n", strings.ToUpper(q.Veredicto), razoes)
	}
	if f := r.FormaNoFornecedor; f != nil {
		fmt.Fprintf(&b, "  FORMA NO PROVIDER FALSO (wire %s; so nomes e tipos, nunca valores): %d turno(s) com tool calls recebidos de volta\n", f.Wire, f.Turnos)
		blocoDeContagens(&b, "  forma do assistant com tool calls", f.Assistant)
		blocoDeContagens(&b, "  o estado que voltou, por campo", f.Estado)
		blocoDeContagens(&b, "  respostas 400 do provider falso, por causa", f.Recusas)
	}

	if len(r.PorBraco) > 1 {
		for _, br := range r.PorBraco {
			fmt.Fprintf(&b, "\nBRACO %s - %s (n = %d runs)\n", br.Braco, br.Descricao, br.Taxas.N)
			blocoDeTaxas(&b, br.Taxas)
			blocoDeContagens(&b, "desfechos", br.Taxas.Desfechos)
		}
	}
	if len(r.Comparacoes) > 0 {
		b.WriteString("\nCOMPARACOES COM O BRACO A (taxa de primeiras tentativas sem tool call)\n")
		for _, c := range r.Comparacoes {
			if c.Diferenca == nil {
				fmt.Fprintf(&b, "  %s contra A (%s): sem denominador\n", c.Braco, c.Mede)
				continue
			}
			fmt.Fprintf(&b, "  %s contra A (%s): diferenca %+.1f pontos; intervalos de 95%% sobrepostos: %v; p (Fisher exacto) = %.4f; p corrigido (Holm, %d comparacoes) = %.4f; distingue a 5%%: %v\n",
				c.Braco, c.Mede, 100**c.Diferenca, *c.IntervalosSobrepostos, *c.PValor, len(r.Comparacoes), *c.PValorCorrigido, *c.DistingueA5)
		}
	}
	if len(r.PorCaso) > 1 {
		b.WriteString("\nPOR CASO\n")
		for _, c := range r.PorCaso {
			fmt.Fprintf(&b, " %s (%d runs)\n", c.Caso, c.Taxas.N)
			blocoDeContagens(&b, "desfechos", c.Taxas.Desfechos)
			blocoDaDevolucao(&b, c.Taxas.Devolucao)
		}
	}
	b.WriteString("\nO QUE ESTA CORRIDA NAO PROVA\n")
	for _, l := range r.Limites {
		b.WriteString("  - " + l + "\n")
	}
	return b.String()
}
