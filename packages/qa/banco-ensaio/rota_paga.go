package bancoensaio

import (
	"context"
	"fmt"
	"io"
	"path/filepath"

	modelgateway "github.com/aos-ref/platform/model-gateway"
)

// A ROTA PAGA DE UMA EXPERIÊNCIA DE MEDIA (AOS-537). O braço de visão da experiência `ocr`
// (AOS-529/530) e o da experiência `video` seguem as MESMAS regras do modo real, e por isso vivem
// aqui uma só vez: o ficheiro de chaves do dono (explícito), a lista de modelos dele, o perfil
// medido (o `expected_model` tem de ser a rota da corrida), a tabela de preços, o contador do dia
// (com a reconstrução), as qualificações do dia quando há perfil, o pior caso a caber no tecto
// ANTES de enviar, o `--so-plano` com o destino da chave, a varredura dos órfãos e o proxy
// efémero com alias próprio. Extraído do `prepararVisao` do AOS-529 sem mudar uma mensagem dele.

// planoDaRotaPaga é o que uma experiência diz da sua corrida paga.
type planoDaRotaPaga struct {
	// prefixo abre cada mensagem (`aos-ensaio ocr`, `aos-ensaio video`).
	prefixo string
	// chaves, fornecedor, modelo, precos e saida são as opções da linha de comandos.
	chaves, fornecedor, modelo, precos, saida string
	reconstruir                               bool
	// perfil é o de `--perfil` (nil sem ele): com ele a corrida qualifica um perfil.
	perfil *modelgateway.RouteProfile
	// piorCaso são os pedidos máximos, com a sonda; unidade diz o que são os outros (`imagens`,
	// `videos`) e nota acrescenta ao plano o que fica fora (`; o PDF fica fora`).
	piorCaso       int64
	unidade, nota  string
	ticketDoPerfil string
	// alias é o `model_name` da rota no proxy.
	alias string
}

// rotaPaga é a corrida paga preparada: o contador aberto, a rota e, depois de [rotaPaga.levantar],
// o proxy vivo.
type rotaPaga struct {
	// registar conta a corrida entre as qualificações do dia, quando ela qualifica um perfil; nil
	// sem `--perfil`.
	registar   func() (OrdemDaQualificacao, error)
	contador   *Contador
	fornecedor Fornecedor
	modelo     string
	piorCaso   int64
	precos     *TabelaDePrecos
	// pasta é a pasta dos relatórios quando não vem por --saida: a do dono, como no modo real.
	pasta string
	rota  RotaReal
	alias string
	// base e credencial são a raiz da API do proxy vivo e a sua chave mestra (um segredo).
	base, credencial string
	fecharProx       func()
}

func (v *rotaPaga) fechar() {
	if v.fecharProx != nil {
		v.fecharProx()
	}
	v.contador.Fechar()
}

// planearRotaPaga lê as chaves, abre o contador, confere o perfil, as qualificações do dia e o
// pior caso. Não levanta o proxy nem envia nada. Devolve nil e o código de saída quando recusa.
func planearRotaPaga(p planoDaRotaPaga, amb Ambiente, red *Redactor, stderr io.Writer) (*rotaPaga, int) {
	esperado := perfilEsperado(p.perfil)
	caminho := p.chaves
	if caminho == "" {
		caminho = amb.Getenv(EnvDasChaves)
	}
	if caminho == "" {
		fmt.Fprintf(stderr, "%s: RECUSADO — o motor visao exige o caminho do ficheiro de chaves (--chaves ou %s)\n", p.prefixo, EnvDasChaves)
		return nil, SaidaRecusada
	}
	fornecedor, err := LerFornecedor(p.fornecedor)
	if err != nil {
		fmt.Fprintf(stderr, "%s: --visao-fornecedor: %v\n", p.prefixo, err)
		return nil, SaidaUso
	}
	chaves, err := LerChaves(caminho)
	if err != nil {
		fmt.Fprintf(stderr, "%s: RECUSADO — %v\n", p.prefixo, err)
		return nil, SaidaRecusada
	}
	// A lista de modelos do dono manda: um modelo fora dela é recusado por [Chaves.Rota].
	rota, err := chaves.Rota(fornecedor, p.modelo, Destino{deTeste: amb.destinoDeTeste})
	if err != nil {
		fmt.Fprintf(stderr, "%s: RECUSADO — %v\n", p.prefixo, err)
		return nil, SaidaRecusada
	}
	red.Acrescentar(rota.Segredos()...)
	// O PERFIL MEDIDO É O DESTA ROTA (AOS-530), como no `qualificar` (cli.go): o proxy declara ter
	// servido `<prefixo>/<modelo>`, e o gateway do nó compara-o com o `expected_model` do perfil em
	// cada chamada da tool. Um relatório que mediu outro modelo não pode qualificar este perfil.
	if esperado != "" && esperado != prefixoDoProxy(fornecedor)+"/"+rota.Modelo {
		fmt.Fprintf(stderr, "%s: RECUSADO — o expected_model do perfil nao e a rota desta corrida (%s/%s): o veredicto e da rota medida, e o modelo servido tem de ser o que o perfil espera. Nenhum pedido foi enviado\n", p.prefixo, prefixoDoProxy(fornecedor), rota.Modelo)
		return nil, SaidaRecusada
	}
	var precos *TabelaDePrecos
	if p.precos != "" {
		if precos, err = LerTabelaDePrecos(p.precos); err != nil {
			fmt.Fprintf(stderr, "%s: RECUSADO — %v\n", p.prefixo, err)
			return nil, SaidaRecusada
		}
	}
	pastaDoEstado := filepath.Dir(caminho)
	if amb.PastaDoDono != "" {
		pastaDoEstado = amb.PastaDoDono
	}
	caminhoDoContador := filepath.Join(pastaDoEstado, "contador.json")
	pastaDaSaida := p.saida
	if pastaDaSaida == "" {
		pastaDaSaida = filepath.Join(pastaDoEstado, "relatorios")
	}
	pastas := []string{filepath.Join(pastaDoEstado, "relatorios"), filepath.Join(filepath.Dir(caminho), "relatorios"), pastaDaSaida}
	if !verificarContadorDoDia(caminhoDoContador, pastas, []Fornecedor{fornecedor}, amb.Relogio().UTC().Format("2006-01-02"), p.reconstruir, stderr) {
		return nil, SaidaRecusada
	}
	// FAIL-CLOSED: sem tecto, sem preço para um tecto em dólares, ou com o contador em uso, não abre.
	contador, err := AbrirContador(caminhoDoContador, fornecedor, rota.Tectos, precos, rota.Modelo, amb.Relogio)
	if err != nil {
		fmt.Fprintf(stderr, "%s: RECUSADO — %v\n", p.prefixo, err)
		return nil, SaidaRecusada
	}
	v := &rotaPaga{contador: contador, fornecedor: fornecedor, modelo: rota.Modelo, piorCaso: p.piorCaso, precos: precos, pasta: pastaDaSaida, rota: rota, alias: p.alias}
	// AS QUALIFICAÇÕES DO DIA (AOS-518, decisão D7), também para a corrida de media que qualifica
	// um perfil: repetir até passar é escolher o resultado. É o MESMO mecanismo e o MESMO registo
	// das qualificações dos candidatos (`qualificacoes.json`, ao lado do contador e sob a trava
	// dele): duas por perfil e quatro por rota — fornecedor e modelo — por dia UTC, contadas juntas
	// com as da experiência `qualificacao`. Juntas porque o tecto é sobre a ROTA: medir o mesmo
	// modelo noutra experiência não é uma tentativa nova. Confere-se aqui, antes do proxy e de
	// qualquer pedido; escreve-se quando a sonda passa ([rotaPaga.depoisDaSonda]).
	if p.perfil != nil {
		caminhoDasQualificacoes := filepath.Join(pastaDoEstado, NomeDoRegistoDeQualificacoes)
		chave := ChaveDaQualificacao{Dia: amb.Relogio().UTC().Format("2006-01-02"), PerfilDigest: p.perfil.Digest(), Fornecedor: string(fornecedor), Modelo: rota.Modelo}
		feitas, qerr := QualificacoesDeHoje(caminhoDasQualificacoes, pastas, chave)
		if qerr == nil && feitas.esgotada() {
			qerr = ErrQualificacoesDoDia
		}
		if qerr != nil {
			contador.Fechar()
			fmt.Fprintf(stderr, "%s: RECUSADO — %v. Nenhum pedido foi enviado\n", p.prefixo, qerr)
			return nil, SaidaRecusada
		}
		fmt.Fprintf(stderr, "%s: QUALIFICACAO de um perfil (%s): este perfil ja foi qualificado %d vez(es) hoje, de um maximo de %d; esta rota, %d de %d (contadas com as da experiencia qualificacao)\n",
			p.prefixo, p.ticketDoPerfil, feitas.DoPerfil, QualificacoesPorPerfilPorDia, feitas.DaRota, QualificacoesPorRotaPorDia)
		v.registar = func() (OrdemDaQualificacao, error) {
			return RegistarQualificacao(caminhoDasQualificacoes, pastas, chave)
		}
	}
	if err := contador.Cabe(v.piorCaso); err != nil {
		contador.Fechar()
		fmt.Fprintf(stderr, "%s: RECUSADO — o pior caso (%d pedidos: %d %s e 1 sonda) nao cabe: %v\n", p.prefixo, v.piorCaso, v.piorCaso-1, p.unidade, err)
		return nil, SaidaRecusada
	}
	estado, err := contador.Estado()
	if err != nil {
		contador.Fechar()
		fmt.Fprintf(stderr, "%s: RECUSADO — %v\n", p.prefixo, err)
		return nil, SaidaRecusada
	}
	fmt.Fprintf(stderr, "%s: motor VISAO — fornecedor %s, modelo %s; pior caso %d pedidos (%d %s e 1 sonda%s); hoje (%s UTC) restam %d de %d\n",
		p.prefixo, fornecedor, rota.Modelo, v.piorCaso, v.piorCaso-1, p.unidade, p.nota, estado.Dia, estado.PedidosRestantes, estado.TectoPedidos)
	fmt.Fprintf(stderr, "%s: DESTINO DA CHAVE: %s  (contador: %s)\n", p.prefixo, rota.Destino, caminhoDoContador)
	return v, SaidaOK
}

// levantar varre os órfãos de uma corrida anterior e levanta o proxy efémero com a rota do alias.
// Devolve [SaidaOK] ou o código de saída, já dito; numa recusa, o contador fica fechado.
func (v *rotaPaga) levantar(ctx context.Context, prefixo string, amb Ambiente, red *Redactor, stderr io.Writer) int {
	if amb.Lancador == nil {
		contentores, redes, verr := VarrerOrfaos(ctx)
		if verr != nil {
			v.contador.Fechar()
			fmt.Fprintf(stderr, "%s: %v\n", prefixo, verr)
			return SaidaSemInfra
		}
		if contentores+redes > 0 {
			fmt.Fprintf(stderr, "%s: VARRIDOS %d contentor(es) e %d rede(s) aos512-* de uma corrida anterior\n", prefixo, contentores, redes)
		}
	}
	pedido := PedidoDeProxy{Prefixo: prefixoDoProxy(v.fornecedor), Modelo: v.modelo, Alias: v.alias, Vida: VidaMaximaDoProxy}.ComSegredos(v.rota.apiKey, v.rota.apiBase)
	vivo, err := lancadorDe(amb, red).Lancar(ctx, pedido)
	if err != nil {
		v.contador.Fechar()
		fmt.Fprintf(stderr, "%s: %v\n", prefixo, err)
		return SaidaSemInfra
	}
	red.Acrescentar(vivo.ChaveMestra())
	v.fecharProx = vivo.Fechar
	v.base, v.credencial = vivo.BaseURL, vivo.ChaveMestra()
	return SaidaOK
}

// depoisDaSonda decide a corrida pela sonda da rota, feita antes do primeiro pedido: sem 200 a
// corrida não começa; com 200 a corrida conta entre as qualificações do dia, quando qualifica um
// perfil.
func (v *rotaPaga) depoisDaSonda(s Sonda, prefixo string, stderr io.Writer) int {
	if s.Resultado == SondaOK {
		if v.registar != nil {
			if _, err := v.registar(); err != nil {
				fmt.Fprintf(stderr, "%s: RECUSADO — %v\n", prefixo, err)
				return SaidaRecusada
			}
		}
		return SaidaOK
	}
	fmt.Fprintf(stderr, "%s: a SONDA nao teve 200 (HTTP %d, %s): a corrida NAO COMECOU; gastou-se no maximo 1 pedido do tecto\n", prefixo, s.HTTP, s.Resultado)
	if s.Resultado == SondaTecto || s.Resultado == SondaContador {
		return SaidaRecusada
	}
	return SaidaParouAMeio
}

// uso é o que o relatório de uma corrida paga leva com os nomes de campo das corridas
// reais, para o contador do dia se reconstruir também a partir dele.
func (v *rotaPaga) uso(amb Ambiente, custo func() (int64, bool)) (*RotaDoOCR, *PedidosDoOCR, *CustoDoOCR) {
	rota := &RotaDoOCR{Fornecedor: string(v.fornecedor), Modelo: v.modelo}
	pedidos := &PedidosDoOCR{PiorCaso: v.piorCaso, DiaUTC: amb.Relogio().UTC().Format("2006-01-02")}
	c := &CustoDoOCR{}
	if total, ok := custo(); ok {
		c.MicroUSDDaCorrida = &total
	}
	if e, err := v.contador.Estado(); err == nil {
		pedidos.Enviados = e.EnviadosNestaCorrida
		if e.TemUSD {
			hoje := e.MicroUSDHoje
			c.MicroUSDHoje = &hoje
		}
	}
	return rota, pedidos, c
}
