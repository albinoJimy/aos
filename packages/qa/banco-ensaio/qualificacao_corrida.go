package bancoensaio

import (
	"context"
	"fmt"
	"time"

	modelgateway "github.com/aos-ref/platform/model-gateway"
)

// A CORRIDA DE QUALIFICAÇÃO (AOS-518): o que a experiência `qualificacao` acrescenta à corrida da
// bateria — o tecto próprio, a corrida de controlo sem devolução, e o bloco do relatório com o
// veredicto. A regra está em qualificacao.go; aqui só se junta o que ela lê.

// RecusasDoPerfilQueAbortam é o número de respostas 4xx de parâmetro seguidas, desde o primeiro
// pedido, que pára uma qualificação: o provider não aceita o perfil, e continuar só gastava o tecto.
const RecusasDoPerfilQueAbortam = 3

// CfgDaQualificacao configura a experiência `qualificacao`.
type CfgDaQualificacao struct {
	// ModeloEsperado é o `expected_model` do perfil candidato: o que o proxy deve declarar ter
	// servido em cada turno.
	ModeloEsperado string
	// ImagemDoProxy é a imagem do proxy atrás da qual a corrida é feita, fixada pelo digest. Vazia
	// no modo falso, que não tem proxy. Nos outros modos tem de ser a declarada para produção.
	ImagemDoProxy string
	// Limite é o tecto próprio de pedidos. Obrigatório: tem de ser o MESMO que o transporte do nó
	// de ensaio — e o do nó de controlo — consulta antes de cada pedido.
	Limite *LimiteDaQualificacao
	// TectoMicroUSD é o tecto de gasto estimado da qualificação. Só actua quando o contador do
	// dia estima o gasto (o fornecedor tem tecto em dólares).
	TectoMicroUSD int64
	// ControloAplicavel diz que o perfil declara devolução OBRIGATÓRIA: a qualificação inclui a
	// corrida de controlo sem devolução. Controlo é o nó de ensaio montado com o perfil de
	// controlo ([PerfilDeControlo]), e ControloPerfilDigest o digest desse perfil.
	ControloAplicavel    bool
	Controlo             *NoDeEnsaio
	ControloPerfilDigest string
	// QualificacoesHoje é o número de ordem desta qualificação entre as do mesmo perfil hoje; nil
	// fora do modo real.
	QualificacoesHoje *int

	// microUSDAoComecar é o gasto estimado do dia antes do primeiro pedido desta qualificação.
	microUSDAoComecar int64
}

// PerfilDeControlo devolve o perfil da corrida de controlo de um perfil que devolve estado: igual
// em tudo, menos na devolução — `devolver: nunca`, sem `devolver_em` nem id do provider, e a
// projecção 1.2.0 (a 1.3.0 só existe para devolver). É o perfil com que se pergunta ao fornecedor
// «e se o estado NÃO voltar?».
func PerfilDeControlo(p modelgateway.RouteProfile) modelgateway.RouteProfile {
	p.StateReturn, p.StateReturnAt, p.ToolCallID = modelgateway.StateReturnNever, "", ""
	p.ProjectionVersion = modelgateway.NativeProjectionVersion120
	return p
}

// nosComToolsPorPassagem conta os nós da bateria que exigem pelo menos uma tool.
func nosComToolsPorPassagem(b *Bateria) int {
	n := 0
	for _, c := range b.Casos {
		for _, no := range c.Nos {
			if len(no.Exige) > 0 {
				n++
			}
		}
	}
	return n
}

// planoDoControlo é o plano da corrida de controlo: a bateria inteira, no braço A.
func planoDoControlo(b *Bateria) Plano {
	return PlanoDaBateria(b, BracoA, PassagensDoControlo)
}

// validarQualificacao confere, antes de qualquer pedido, que a configuração da qualificação está
// inteira. Fail-closed: o que faltar recusa a corrida.
func (cfg CfgDaCorrida) validarQualificacao() error {
	q := cfg.Qualificacao
	if (cfg.Plano.Experiencia == ExperienciaQualificacao) != (q != nil) {
		return fmt.Errorf("%w: a configuracao da qualificacao existe na experiencia qualificacao, e so nela", ErrPlano)
	}
	if q == nil {
		return nil
	}
	if cfg.PerfilDigest == "" || q.ModeloEsperado == "" || !cfg.No.qualifica {
		return ErrQualificacaoSemPerfil
	}
	if q.Limite == nil || cfg.No.limite != q.Limite {
		return fmt.Errorf("%w: a qualificacao nao corre sem o seu tecto de pedidos no transporte do no de ensaio", ErrPlano)
	}
	if nosComToolsPorPassagem(cfg.Bateria)*PassagensDaQualificacao != NosComToolsDaQualificacao {
		return fmt.Errorf("%w: a bateria ja nao da %d nos com tools em %d passagens; os limiares valem para a bateria que citam", ErrPlano, NosComToolsDaQualificacao, PassagensDaQualificacao)
	}
	if q.ControloAplicavel && (q.Controlo == nil || !q.Controlo.qualifica || q.Controlo.limite != q.Limite || q.Controlo.devolucao != nil || q.ControloPerfilDigest == "") {
		return fmt.Errorf("%w: um perfil com devolucao obrigatoria qualifica-se com a corrida de controlo sem devolucao", ErrPlano)
	}
	if cfg.Modo != ModoFalso {
		if err := conferirImagemDoProxy(q.ImagemDoProxy, ImagemDoProxyDeProducao); err != nil {
			return err
		}
	}
	return nil
}

// pedidosDoControlo é o pior caso da corrida de controlo (zero quando não há).
func (cfg CfgDaCorrida) pedidosDoControlo() int64 {
	if cfg.Qualificacao == nil || !cfg.Qualificacao.ControloAplicavel {
		return 0
	}
	n, _ := planoDoControlo(cfg.Bateria).PedidosMaximos(cfg.Bateria)
	return n
}

// paragemDaQualificacao devolve a pergunta que o ciclo faz depois de cada run: se o gasto estimado
// desta qualificação já chegou ao seu tecto em dólares. nil fora da qualificação.
func (cfg CfgDaCorrida) paragemDaQualificacao() func() string {
	q := cfg.Qualificacao
	if q == nil {
		return nil
	}
	if cfg.Contador != nil {
		if e, err := cfg.Contador.Estado(); err == nil {
			q.microUSDAoComecar = e.MicroUSDHoje
		}
	}
	return func() string {
		if cfg.Contador == nil || q.TectoMicroUSD <= 0 {
			return ""
		}
		e, err := cfg.Contador.Estado()
		if err != nil {
			return TerminouContador
		}
		if e.TemUSD && e.MicroUSDHoje-q.microUSDAoComecar >= q.TectoMicroUSD {
			return TerminouTectoDaQualificacao
		}
		return ""
	}
}

// terminouDaQualificacao distingue o tecto próprio da qualificação do tecto do dia.
func (cfg CfgDaCorrida) terminouDaQualificacao(terminou string) string {
	if q := cfg.Qualificacao; q != nil && terminou == TerminouTecto && q.Limite != nil && q.Limite.Atingido() {
		return TerminouTectoDaQualificacao
	}
	return terminou
}

// corridaDeControlo é o que a corrida de controlo deixou.
type corridaDeControlo struct {
	terminou string
	obs      []Observacao
}

// correrControlo corre a corrida de controlo sem devolução, quando o perfil a pede E a corrida
// principal deu `qualificado` — com outro veredicto o controlo não mudava nada e gastava tecto.
// Devolve nil quando não correu.
func (cfg CfgDaCorrida) correrControlo(ctx context.Context, terminou string, obs []Observacao, parar func() string) *corridaDeControlo {
	q := cfg.Qualificacao
	if q == nil || !q.ControloAplicavel || q.Controlo == nil {
		return nil
	}
	if qualificarModelo(contarParaQualificacao(terminou, NosComToolsDaQualificacao, obs)).Veredicto != VeredictoQualificado {
		return nil
	}
	if cfg.Pausa > 0 {
		select {
		case <-ctx.Done():
		case <-time.After(cfg.Pausa):
		}
	}
	doControlo, fim := cicloDaCorrida(ctx, cfg, q.Controlo, planoDoControlo(cfg.Bateria), parar)
	return &corridaDeControlo{terminou: cfg.terminouDaQualificacao(fim), obs: doControlo}
}

// Os resultados da corrida de controlo sem devolução. Vocabulário FECHADO.
const (
	// ControloFalhouComoDevido — sem o estado de volta, o fornecedor respondeu 4xx de parâmetro a
	// pedidos com mensagem tool: EXIGE o estado, e a corrida principal mostra que o aceitou.
	ControloFalhouComoDevido = "falhou_como_devido"
	// ControloPassou — sem o estado de volta correu tudo bem: o fornecedor NÃO exige o estado, e
	// a corrida principal não prova que ele o lê.
	ControloPassou = "passou"
	// ControloInconclusivo — o controlo correu e não deu nem uma coisa nem outra.
	ControloInconclusivo = "inconclusivo"
	// ControloNaoCorreu — a corrida principal não deu `qualificado`, e o controlo não foi feito.
	ControloNaoCorreu = "nao_correu"
)

// Os avisos de uma qualificação. Vocabulário FECHADO. Um aviso NÃO muda o veredicto (decisão D2);
// vai ao lado dele no relatório e à cabeça do resumo, e não pode ser omitido.
const (
	// AvisoExigenciaNaoProvada — o perfil declara devolução obrigatória e o controlo sem
	// devolução não falhou como devia: «aceite» não é «exigido».
	AvisoExigenciaNaoProvada = "exigencia_nao_provada"
	// AvisoDevolucaoNaoCumprida — o perfil devolve estado e o veredicto da devolução
	// (`qualificacao_da_devolucao`) não é `cumprida`.
	AvisoDevolucaoNaoCumprida = "devolucao_do_estado_nao_cumprida"
)

// ControloSemDevolucao é o registo da corrida de controlo de um perfil com devolução obrigatória.
type ControloSemDevolucao struct {
	// Resultado é um de `falhou_como_devido`, `passou`, `inconclusivo`, `nao_correu`.
	Resultado string `json:"resultado"`
	// Passagens pela bateria da corrida de controlo; DigestDoPerfil, o do perfil de controlo.
	Passagens      int    `json:"passagens"`
	DigestDoPerfil string `json:"digest_do_perfil_de_controlo"`
	// Contagens são as da corrida de controlo, na mesma forma das da corrida principal. Ausentes
	// quando o controlo não correu.
	Contagens *ContagensDaQualificacao `json:"contagens,omitempty"`
}

// resultadoDoControlo lê o resultado de um controlo que correu. É função pura das suas contagens.
func resultadoDoControlo(c ContagensDaQualificacao) string {
	switch {
	case c.QuatroXXComMensagemTool > 0:
		return ControloFalhouComoDevido
	case c.Terminou == TerminouCompleta && !c.amostraIncompleta() && c.naoCumpridos() == 0:
		return ControloPassou
	}
	return ControloInconclusivo
}

// avisosDaQualificacao devolve os avisos, numa ordem fixa.
func avisosDaQualificacao(controlo *ControloSemDevolucao, devolucao *QualificacaoDaDevolucao) []string {
	avisos := []string{}
	if controlo != nil && controlo.Resultado != ControloFalhouComoDevido {
		avisos = append(avisos, AvisoExigenciaNaoProvada)
	}
	if devolucao != nil && devolucao.Veredicto != QualificacaoCumprida {
		avisos = append(avisos, AvisoDevolucaoNaoCumprida)
	}
	return avisos
}

// O que uma qualificação NÃO prova. Vocabulário FECHADO; as frases correspondentes estão nos
// limites do relatório.
const (
	NaoProvaBateriaSintetica = "bateria_de_seis_casos_sinteticos_e_documentos_curtos"
	NaoProvaLeitura          = "cumprido_e_aceite_sao_2xx_e_nao_provam_que_o_fornecedor_leu"
	NaoProvaAmostra          = "amostra_de_40_nos_sem_falhas_so_afirma_taxa_de_falha_abaixo_de_cerca_de_9_por_cento_a_95"
	NaoProvaQueEBom          = "apanha_um_modelo_mau_e_nao_mede_se_e_bom"
	NaoProvaACorrida         = "o_digest_prova_que_o_relatorio_nao_foi_alterado_e_nao_que_a_corrida_aconteceu"
	NaoProvaOutraRota        = "vale_so_para_esta_rota_modelo_fornecedor_imagem_do_proxy_e_perfil"
	NaoProvaProxyDeProducao  = "o_proxy_do_ensaio_nao_tem_a_configuracao_de_producao"
	NaoProvaModeloReal       = "o_fornecedor_e_um_provider_falso_e_o_veredicto_nao_vale_para_producao"
)

func naoProvaDaQualificacao(modo string) []string {
	l := []string{NaoProvaBateriaSintetica, NaoProvaLeitura, NaoProvaAmostra, NaoProvaQueEBom, NaoProvaACorrida, NaoProvaOutraRota}
	if modo != ModoFalso {
		l = append(l, NaoProvaProxyDeProducao)
	}
	if modo != ModoReal {
		l = append(l, NaoProvaModeloReal)
	}
	return l
}

// limitesDaQualificacao são as frases, fixas, do que uma qualificação não prova.
func limitesDaQualificacao() []string {
	return []string{
		"Qualificacao: a bateria sao seis casos sinteticos com documentos curtos. O veredicto apanha um modelo MAU; nao mede se e bom — isso mede-se com a fatia em producao.",
		"Qualificacao: `cumprido` e o veredicto do kernel sobre o contrato de conclusao do no, e `aceite` e uma resposta 2xx. Nenhum dos dois prova que o fornecedor LEU o que lhe foi enviado.",
		"Qualificacao: com 40 nos com tools e zero falhas, o que se pode afirmar com 95% de confianca e que a taxa de falha esta abaixo de cerca de 9%. O intervalo de confianca vai ao lado de cada contagem e NAO entra na regra: os limiares aplicam-se as contagens.",
		"Qualificacao: o veredicto vale para ESTA rota — modelo, fornecedor, imagem do proxy e perfil — e para esta versao da bateria. Mudar um deles e qualificar outra vez.",
		"Qualificacao: o digest do relatorio prova que o relatorio nao foi alterado depois de escrito. Nao prova que a corrida aconteceu nem quem a correu: o relatorio e produzido no posto de quem corre e nao e assinado pelo banco.",
	}
}

// RotaQualificada é tudo o que identifica o que foi qualificado.
type RotaQualificada struct {
	Fornecedor string `json:"fornecedor"`
	Modelo     string `json:"modelo"`
	// ModeloEsperado é o `expected_model` do perfil: o que o proxy tinha de declarar ter servido.
	ModeloEsperado string `json:"modelo_esperado"`
	DigestDaRota   string `json:"digest_da_rota"`
	DigestDoPerfil string `json:"digest_do_perfil"`
	// DigestDaBateria e VersaoDaBateria: o veredicto vale para a bateria que cita.
	DigestDaBateria string `json:"digest_da_bateria"`
	VersaoDaBateria string `json:"versao_da_bateria"`
	// ImagemDoProxy é a imagem do proxy atrás da qual a corrida foi feita, e DigestDaImagemDoProxy
	// o seu digest. Ausentes no modo falso, que não tem proxy.
	ImagemDoProxy         string `json:"imagem_do_proxy,omitempty"`
	DigestDaImagemDoProxy string `json:"digest_da_imagem_do_proxy,omitempty"`
}

// QualificacaoDoModelo é o bloco do relatório de uma qualificação: o veredicto, de que contagens
// saiu, o que identifica a rota, o controlo, o orçamento e o que a corrida não prova.
type QualificacaoDoModelo struct {
	// Veredicto, Razoes e Regra: ver [VeredictoDoModelo]. É ESTE campo que se lê.
	Veredicto string   `json:"veredicto"`
	Razoes    []string `json:"razoes"`
	// Avisos não mudam o veredicto e não podem ser omitidos ([AvisoExigenciaNaoProvada]).
	Avisos []string `json:"avisos"`
	Regra  string   `json:"regra"`
	// Limiares é a tabela L1 a L5: contagem, amostra, intervalo de confiança e limiar.
	Limiares []LimiarDaQualificacao `json:"limiares"`
	// Contagens são as contagens de que o veredicto é função.
	Contagens ContagensDaQualificacao `json:"contagens"`
	Rota      RotaQualificada         `json:"rota_qualificada"`
	// Controlo existe quando o perfil declara devolução obrigatória.
	Controlo  *ControloSemDevolucao   `json:"controlo_sem_devolucao,omitempty"`
	Orcamento OrcamentoDaQualificacao `json:"orcamento"`
	// NaoProva diz, em vocabulário fechado, o que a corrida não prova.
	NaoProva []string `json:"nao_prova"`
}

// montarQualificacao calcula o bloco da qualificação e põe-no no relatório. Chama-se com o resto
// do relatório já construído (lê os pedidos enviados e o veredicto da devolução).
func montarQualificacao(cfg CfgDaCorrida, r *Relatorio, previstos int64, obs []Observacao, controlo *corridaDeControlo) error {
	q := cfg.Qualificacao
	contagens := contarParaQualificacao(r.Terminou, NosComToolsDaQualificacao, obs)
	veredicto := qualificarModelo(contagens)
	bloco := &QualificacaoDoModelo{
		Veredicto: veredicto.Veredicto, Razoes: veredicto.Razoes, Regra: veredicto.Regra,
		Limiares: limiaresDaQualificacao(contagens), Contagens: contagens,
		Rota: RotaQualificada{
			Fornecedor: cfg.Rota.Fornecedor, Modelo: cfg.Rota.Modelo, ModeloEsperado: q.ModeloEsperado,
			DigestDaRota: cfg.Rota.Digest, DigestDoPerfil: cfg.PerfilDigest,
			DigestDaBateria: cfg.Bateria.Digest(), VersaoDaBateria: cfg.Bateria.Versao,
			ImagemDoProxy: q.ImagemDoProxy, DigestDaImagemDoProxy: digestDaImagem(q.ImagemDoProxy),
		},
		Orcamento: OrcamentoDaQualificacao{
			PiorCasoDeclarado: previstos, TectoMicroUSD: q.TectoMicroUSD,
			PedidosEnviados: r.Pedidos.Enviados, QualificacoesDoPerfilHoje: q.QualificacoesHoje,
			MaximoPorPerfilPorDia: QualificacoesPorPerfilPorDia,
		},
		NaoProva: naoProvaDaQualificacao(cfg.Modo),
	}
	if cfg.Sondar {
		bloco.Orcamento.PiorCasoDeclarado++
	}
	if q.Limite != nil {
		bloco.Orcamento.TectoDePedidos = q.Limite.maximo
	}
	if cfg.Contador != nil {
		e, err := cfg.Contador.Estado()
		if err != nil {
			return err
		}
		if e.TemUSD {
			gasto := e.MicroUSDHoje - q.microUSDAoComecar
			bloco.Orcamento.MicroUSDEstimado = &gasto
		}
	}
	if q.ControloAplicavel {
		c := &ControloSemDevolucao{Resultado: ControloNaoCorreu, Passagens: PassagensDoControlo, DigestDoPerfil: q.ControloPerfilDigest}
		if controlo != nil {
			doControlo := contarParaQualificacao(controlo.terminou, nosComToolsPorPassagem(cfg.Bateria)*PassagensDoControlo, controlo.obs)
			c.Contagens, c.Resultado = &doControlo, resultadoDoControlo(doControlo)
		}
		bloco.Controlo = c
	}
	bloco.Avisos = avisosDaQualificacao(bloco.Controlo, r.Qualificacao)
	r.QualificacaoDoModelo = bloco
	return nil
}

// RelatorioDaQualificacaoQueNaoCoube devolve o relatório de uma qualificação cujo pior caso não
// cabia no que restava do tecto do dia: sem observações, com o veredicto `inconclusivo`. Nenhum
// pedido saiu, e por isso não precisa de nó de ensaio.
func RelatorioDaQualificacaoQueNaoCoube(cfg CfgDaCorrida) (*Relatorio, error) {
	if cfg.Qualificacao == nil || cfg.Bateria == nil {
		return nil, fmt.Errorf("%w: sem a configuracao da qualificacao", ErrPlano)
	}
	previstos, err := cfg.Plano.PedidosMaximos(cfg.Bateria)
	if err != nil {
		return nil, err
	}
	relogio := cfg.Relogio
	if relogio == nil {
		relogio = time.Now
	}
	return construirRelatorio(cfg, relogio().UTC(), previstos+cfg.pedidosDoControlo(), TerminouNaoCoube, nil, nil, nil)
}
