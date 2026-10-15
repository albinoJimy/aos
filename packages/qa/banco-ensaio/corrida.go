package bancoensaio

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
)

// A CORRIDA: o plano (que runs, por que ordem), a verificação de que cabe no tecto, e o ciclo.

// Experiencia é o que uma corrida faz. Vocabulário fechado.
type Experiencia string

const (
	// ExperienciaBateria — a bateria T1 a T6, num braço.
	ExperienciaBateria Experiencia = "bateria"
	// ExperienciaSeparadores — a experiência dos separadores: os quatro braços sobre o caso T1,
	// intercalados, um só turno por amostra.
	ExperienciaSeparadores Experiencia = "separadores"
	// ExperienciaQualificacao — a qualificação de uma rota (AOS-518): a bateria oito vezes, no
	// braço A, com um perfil candidato, e um veredicto calculado. Ver qualificacao.go.
	ExperienciaQualificacao Experiencia = "qualificacao"
)

// AmostrasDosSeparadores é o tamanho de amostra por braço da experiência dos separadores:
// distingue 10% de 32% com 80% de potência a 5% (desenho A2 §5.3). Efeitos menores não se vêem.
const AmostrasDosSeparadores = 53

// CasoDosSeparadores é o caso sobre o qual a experiência dos separadores corre.
const CasoDosSeparadores = "T1"

// Plano é o plano de uma corrida.
type Plano struct {
	Experiencia Experiencia `json:"experiencia"`
	// Amostras é o número de amostras por braço (separadores) ou de passagens pela bateria.
	Amostras int `json:"amostras"`
	// Semente fixa a ordem dos braços dentro de cada bloco. Vai no relatório.
	Semente uint64 `json:"semente"`
	// Tentativas é o máximo de tentativas por nó (1 = sem nova tentativa).
	Tentativas int `json:"tentativas"`
	// MaxTurnos é o tecto de turnos — e de pedidos — de cada run.
	MaxTurnos int `json:"max_turnos"`
	// Casos são os ids dos casos a correr, pela ordem.
	Casos []string `json:"casos"`
	// Bracos são os braços da corrida.
	Bracos []Braco `json:"bracos"`
	// BracosDoConselheiro são os braços da experiência `conselheiro` (AOS-528): [BracoSoExecutor]
	// e, com conselheiro, [BracoComConselheiro]. Vazio nas outras experiências — e ausente do JSON,
	// para o digest da configuração delas não mudar.
	BracosDoConselheiro []string `json:"bracos_do_conselheiro,omitempty"`
	// MaxConsultas é o máximo de consultas ao conselheiro por run do braço com ele; zero sem ele.
	MaxConsultas int `json:"max_consultas,omitempty"`
}

// PlanoDaBateria devolve o plano de uma corrida da bateria inteira num braço.
func PlanoDaBateria(b *Bateria, braco Braco, amostras int) Plano {
	p := Plano{Experiencia: ExperienciaBateria, Amostras: amostras, Tentativas: 3, MaxTurnos: MaxTurnosPorOmissao, Bracos: []Braco{braco}}
	for _, c := range b.Casos {
		p.Casos = append(p.Casos, c.ID)
	}
	return p
}

// PlanoDosSeparadores devolve o plano da experiência dos separadores: quatro braços sobre T1,
// 53 amostras por braço, UM turno e UMA tentativa por amostra — 212 pedidos no total.
//
// Um só turno porque a métrica é do primeiro turno (o modelo pediu a tool pelo mecanismo nativo
// ou não), e assim cada amostra custa exactamente um pedido. Um run que pediu a tool acaba em
// `turnos_esgotados`: é o desfecho esperado do braço que funciona, não uma falha.
func PlanoDosSeparadores(semente uint64) Plano {
	return Plano{
		Experiencia: ExperienciaSeparadores, Amostras: AmostrasDosSeparadores, Semente: semente,
		Tentativas: 1, MaxTurnos: 1, Casos: []string{CasoDosSeparadores}, Bracos: Bracos(),
	}
}

// PlanoDaQualificacao devolve o plano de uma qualificação: a bateria inteira, no braço A,
// [PassagensDaQualificacao] vezes. Não tem parâmetros de propósito — a amostra é a da decisão D2.
func PlanoDaQualificacao(b *Bateria, semente uint64) Plano {
	p := PlanoDaBateria(b, BracoA, PassagensDaQualificacao)
	p.Experiencia, p.Semente = ExperienciaQualificacao, semente
	return p
}

// ErrPlano — o plano não é utilizável.
var ErrPlano = errors.New("banco-ensaio: plano de corrida invalido")

func (p Plano) validar(b *Bateria) error {
	if p.Experiencia != ExperienciaBateria && p.Experiencia != ExperienciaSeparadores && p.Experiencia != ExperienciaQualificacao && p.Experiencia != ExperienciaDegraus && p.Experiencia != ExperienciaConselheiro {
		return fmt.Errorf("%w: experiencia desconhecida", ErrPlano)
	}
	// A bateria difícil (AOS-528) só corre na experiência `conselheiro`, e esta só sobre ela.
	if (p.Experiencia == ExperienciaConselheiro) != b.EDificil() {
		return fmt.Errorf("%w: a experiencia conselheiro corre a bateria dificil, e so ela", ErrPlano)
	}
	if err := p.validarDoConselheiro(b); err != nil {
		return err
	}
	// Cada bateria tem as suas experiências (AOS-525): a por degraus só corre na experiência
	// `degraus`, e as outras experiências só correm sobre a bateria sem degraus.
	if (p.Experiencia == ExperienciaDegraus) != b.TemDegraus() {
		return fmt.Errorf("%w: a experiencia degraus corre a bateria por degraus, e so ela", ErrPlano)
	}
	if p.Experiencia == ExperienciaDegraus && (len(p.Bracos) != 1 || p.Bracos[0] != BracoA || p.Tentativas != 3 || p.MaxTurnos != MaxTurnosDosDegraus || len(p.Casos) != len(b.Casos)) {
		return fmt.Errorf("%w: a experiencia degraus corre a bateria por degraus inteira, no braco A, com 3 tentativas e %d turnos", ErrPlano, MaxTurnosDosDegraus)
	}
	if p.Experiencia == ExperienciaQualificacao && (p.Amostras != PassagensDaQualificacao || len(p.Bracos) != 1 || p.Bracos[0] != BracoA || p.Tentativas != 3 || len(p.Casos) != len(b.Casos)) {
		return fmt.Errorf("%w: a qualificacao corre a bateria inteira, %d vezes, no braco A, com 3 tentativas", ErrPlano, PassagensDaQualificacao)
	}
	if p.Amostras <= 0 || p.Amostras > 10000 || p.Tentativas <= 0 || p.Tentativas > 3 || p.MaxTurnos <= 0 || p.MaxTurnos > 16 {
		return fmt.Errorf("%w: amostras (1 a 10000), tentativas (1 a 3) e turnos (1 a 16) fora dos limites", ErrPlano)
	}
	if len(p.Casos) == 0 || len(p.Bracos) == 0 {
		return fmt.Errorf("%w: sem casos ou sem bracos", ErrPlano)
	}
	for _, id := range p.Casos {
		if _, ok := b.Caso(id); !ok {
			return fmt.Errorf("%w: caso desconhecido %q", ErrPlano, id)
		}
	}
	vistos := map[Braco]bool{}
	for _, br := range p.Bracos {
		if _, err := LerBraco(string(br)); err != nil || vistos[br] {
			return fmt.Errorf("%w: braco desconhecido ou repetido", ErrPlano)
		}
		vistos[br] = true
	}
	return nil
}

// PedidosMaximos é o número MÁXIMO de pedidos que a corrida pode fazer ao modelo EXECUTOR: por
// cada amostra, braço e nó, `Tentativas × MaxTurnos`. Na experiência dos separadores é o número
// exacto. Na experiência `conselheiro` conta os dois braços dela; os pedidos ao conselheiro são os
// de [Plano.PedidosMaximosDoConselheiro].
func (p Plano) PedidosMaximos(b *Bateria) (int64, error) {
	if err := p.validar(b); err != nil {
		return 0, err
	}
	var nos int64
	for _, id := range p.Casos {
		c, _ := b.Caso(id)
		nos += int64(len(c.Nos))
	}
	bracosDoConselheiro := int64(max(1, len(p.BracosDoConselheiro)))
	return int64(p.Amostras) * int64(len(p.Bracos)) * bracosDoConselheiro * nos * int64(p.Tentativas) * int64(p.MaxTurnos), nil
}

// passo é uma unidade da sequência: um caso, num braço, numa amostra.
type passo struct {
	caso    Caso
	braco   Braco
	amostra int
	// conselho é o braço do conselheiro (AOS-528); vazio fora da experiência `conselheiro`.
	conselho string
}

// sequencia devolve a ordem da corrida. Os braços correm INTERCALADOS, em blocos: cada amostra
// é um bloco com todos os braços, numa ordem baralhada pela semente (blocos aleatorizados). Um
// braço nunca corre «de seguida»: uma deriva da rota ao longo da corrida — carga, hora, um
// limite de taxa — reparte-se pelos braços em vez de cair toda num.
func (p Plano) sequencia(b *Bateria) []passo {
	gerador := novoGerador(p.Semente)
	var out []passo
	for amostra := 0; amostra < p.Amostras; amostra++ {
		ordem := append([]Braco(nil), p.Bracos...)
		for i := len(ordem) - 1; i > 0; i-- {
			j := int(gerador.proximo() % uint64(i+1)) // #nosec G115 -- o resto e menor do que i+1, que e um int positivo
			ordem[i], ordem[j] = ordem[j], ordem[i]
		}
		// Os braços do conselheiro (AOS-528) correm também em blocos, baralhados pela mesma semente.
		conselhos := p.ordemDosBracosDoConselheiro(gerador)
		for _, braco := range ordem {
			for _, conselho := range conselhos {
				for _, id := range p.Casos {
					c, _ := b.Caso(id)
					out = append(out, passo{caso: c, braco: braco, amostra: amostra, conselho: conselho})
				}
			}
		}
	}
	return out
}

// gerador é um gerador determinista (splitmix64) para a ordem dos braços. Não é criptográfico
// nem precisa de ser: a semente vai no relatório para a ordem se reproduzir.
type gerador struct{ estado uint64 }

func novoGerador(semente uint64) *gerador { return &gerador{estado: semente} }

func (g *gerador) proximo() uint64 {
	g.estado += 0x9e3779b97f4a7c15
	z := g.estado
	z = (z ^ (z >> 30)) * 0xbf58476d1ce4e5b9
	z = (z ^ (z >> 27)) * 0x94d049bb133111eb
	return z ^ (z >> 31)
}

// Os estados finais de uma corrida.
const (
	TerminouCompleta    = "completa"
	TerminouTecto       = "tecto_atingido"
	TerminouContador    = "contador_inutilizavel"
	TerminouInterrompid = "interrompida"
	// TerminouChaveRecusada — os primeiros pedidos da corrida foram todos recusados por
	// autenticação: a corrida parou para não gastar o tecto com uma chave que não serve.
	TerminouChaveRecusada = "chave_recusada"
	// TerminouSaldoInsuficiente — a conta do fornecedor não tem saldo ou quota: os primeiros
	// pedidos foram todos 429 com esse tipo, ou foi isso que a sonda recebeu.
	TerminouSaldoInsuficiente = "saldo_insuficiente"
	// TerminouLimiteDeRitmo — idem, com o tipo de limite de ritmo: uma pausa entre pedidos resolve.
	TerminouLimiteDeRitmo = "limite_de_ritmo"
	// TerminouSo429 — os primeiros pedidos foram todos 429, com um tipo que o banco não conhece.
	TerminouSo429 = "so_respostas_429"
	// TerminouSerieDe429 — uma série longa de 429 seguidos a meio da corrida.
	TerminouSerieDe429 = "serie_de_429"
	// TerminouModeloDesconhecido — a sonda: o fornecedor não conhece o modelo.
	TerminouModeloDesconhecido = "modelo_desconhecido"
	// TerminouRotaIndisponivel — a sonda: o fornecedor conhece o modelo e não tem endpoint que
	// sirva este pedido com as definições da conta.
	TerminouRotaIndisponivel = "rota_indisponivel"
	// TerminouSondaFalhou — a sonda não teve 200, por uma causa fora do vocabulário.
	TerminouSondaFalhou = "sonda_falhou"
)

// Respostas429QueAbortam é o número de respostas 429 seguidas, desde o primeiro pedido (sem um
// 200 ainda), que faz o modo com modelo real abortar.
const Respostas429QueAbortam = 3

// SerieDe429QueAborta é o comprimento da série de 429 seguidos que, a meio da corrida, a pára.
const SerieDe429QueAborta = 10

// RecusasDeChaveQueAbortam é o número de recusas de autenticação seguidas, desde o primeiro
// pedido, que faz o modo com modelo real abortar.
const RecusasDeChaveQueAbortam = 3

// Os modos de uma corrida.
const (
	ModoFalso = "falso"
	ModoProxy = "proxy"
	ModoReal  = "real"
)

// CfgDaCorrida configura uma corrida.
type CfgDaCorrida struct {
	Modo    string
	Plano   Plano
	Bateria *Bateria
	No      *NoDeEnsaio
	// Contador é o contador do dia. Obrigatório no modo com modelo real: sem ele a corrida
	// recusa arrancar.
	Contador *Contador
	// Rota identifica a rota no relatório.
	Rota RotaDoRelatorio
	// RegiaoDeclarada é a região que o dono declarou; vai para o relatório como declaração.
	RegiaoDeclarada string
	// Extra entra no digest da configuração (o roteiro do falso, a tabela de preços).
	Extra map[string]string
	// PerfilDigest é o digest do perfil de rota candidato da corrida (AOS-513); vai para o
	// relatório. Vazio quando a corrida não leva perfil.
	PerfilDigest string
	// Relogio dá a data do relatório. nil ⇒ time.Now.
	Relogio func() time.Time
	// AbortarAposRecusasDeChave, quando positivo, pára a corrida se os PRIMEIROS pedidos — este
	// número deles, seguidos, desde o início — forem todos recusados por autenticação (401 ou
	// 403): a chave não serve, e continuar só gastava o tecto do dia. Zero ⇒ não aborta.
	AbortarAposRecusasDeChave int
	// AbortarApos429Iniciais, quando positivo, pára a corrida se os PRIMEIROS pedidos — este
	// número deles, desde o início — tiverem todos resposta 429. A causa sai do tipo do erro:
	// saldo insuficiente, limite de ritmo, ou só «429». Zero ⇒ não aborta.
	AbortarApos429Iniciais int
	// AbortarAposSerieDe429, quando positivo, pára a corrida ao fim deste número de respostas 429
	// SEGUIDAS, em qualquer ponto dela. Zero ⇒ não aborta.
	AbortarAposSerieDe429 int
	// Sondar faz UM pedido mínimo à rota antes do primeiro caso ([NoDeEnsaio.Sondar]). Conta no
	// tecto. Se não der 200, a corrida não começa: o relatório sai sem observações, com a causa.
	Sondar bool
	// Pausa é o intervalo entre dois passos da corrida. Zero ⇒ sem pausa. Serve para não bater
	// num limite de taxa do fornecedor; não entra no digest da configuração.
	Pausa time.Duration
	// Progresso, se presente, recebe uma linha a cada vinte passos: só contagens.
	Progresso io.Writer
	// FormaNoFornecedor, se presente, é perguntada no fim da corrida: devolve a FORMA — nunca o
	// conteúdo — com que as mensagens `assistant` com tool calls chegaram ao provider falso
	// (AOS-516). Só os modos sem modelo real a têm: um fornecedor real não a mostra.
	FormaNoFornecedor func() (*FormaNoFornecedor, error)
	// QualificaDevolucao faz o relatório trazer o veredicto da devolução mesmo que o perfil não
	// devolva estado (o controlo negativo contra o provider falso do estado): aí o veredicto é
	// `nao_cumprida`. Com um perfil que devolve estado o veredicto sai sempre.
	QualificaDevolucao bool
	// Qualificacao configura a experiência `qualificacao` (AOS-518). Obrigatória nela, e só nela.
	Qualificacao *CfgDaQualificacao
	// Conselho configura a experiência `conselheiro` (AOS-528): o conselheiro, a rota dele e os
	// preços declarados. Obrigatório nela, e só nela.
	Conselho *CfgDoConselho

	// sondaDoConselheiro é a sonda da rota do conselheiro, quando houve.
	sondaDoConselheiro *Sonda

	// fase, quando presente, é avisada do início da sonda, da corrida e do controlo (AOS-522).
	// É só um relógio: não entra no relatório nem no digest da configuração.
	fase func(nome string)
}

// Os nomes das fases de uma corrida de qualificação, como o relógio do `qualificar` as regista.
const (
	FaseDoPlano      = "plano"
	FaseDaSonda      = "sonda"
	FaseDaCorrida    = "corrida"
	FaseDoControlo   = "controlo"
	FaseDaPreparacao = "preparacao"
)

// marcarFase avisa o relógio, se houver, de que uma fase começou.
func (cfg CfgDaCorrida) marcarFase(nome string) {
	if cfg.fase != nil {
		cfg.fase(nome)
	}
}

// ErrModoRealSemTecto — o modo com modelo real não corre sem contador e tecto do dia.
var ErrModoRealSemTecto = errors.New("banco-ensaio: o modo com modelo real nao corre sem o contador e o tecto do dia")

// Correr corre o plano e devolve o relatório. Um relatório parcial é devolvido também quando a
// corrida pára a meio (tecto atingido, contador inutilizável, interrupção, chave recusada, conta
// sem saldo, limite de ritmo) ou quando a sonda não a deixa começar: [Relatorio.Terminou]
// diz porquê. Só há erro sem relatório quando a corrida nem chegou a arrancar.
func Correr(ctx context.Context, cfg CfgDaCorrida) (*Relatorio, error) {
	if cfg.Bateria == nil || cfg.No == nil {
		return nil, fmt.Errorf("%w: sem bateria ou sem no de ensaio", ErrPlano)
	}
	previstos, err := cfg.Plano.PedidosMaximos(cfg.Bateria)
	if err != nil {
		return nil, err
	}
	if err := cfg.validarQualificacao(); err != nil {
		return nil, err
	}
	if err := cfg.validarConselho(); err != nil {
		return nil, err
	}
	// O pior caso de uma qualificação inclui a corrida de controlo, quando o perfil a pede.
	previstos += cfg.pedidosDoControlo()
	switch cfg.Modo {
	case ModoFalso, ModoProxy:
	case ModoReal:
		// FAIL-CLOSED: o modo real exige o contador, e o MESMO contador tem de ser o que o
		// transporte do nó de ensaio consulta antes de cada pedido.
		if cfg.Contador == nil || cfg.No.contador != cfg.Contador {
			return nil, ErrModoRealSemTecto
		}
		// E o conselheiro, quando há, conta no contador do fornecedor dele: o mesmo ficheiro e a
		// mesma trava do contador da corrida ([Contador.Irmao]).
		if cs := cfg.conselheiro(); cs != nil && (cs.contador == nil || cs.contador.raiz() != cfg.Contador.raiz()) {
			return nil, ErrModoRealSemTecto
		}
	default:
		return nil, fmt.Errorf("%w: modo desconhecido", ErrPlano)
	}
	if cfg.Contador != nil {
		// Antes de começar: a corrida inteira — e a sonda, se houver — tem de caber no que resta
		// do tecto do dia.
		aEnviar := previstos
		if cfg.Sondar {
			aEnviar++
		}
		if err := cfg.Contador.Cabe(aEnviar); err != nil {
			if cfg.Qualificacao != nil && errors.Is(err, ErrNaoCabe) {
				// Uma qualificação que não cabe no tecto não é só recusada: fica escrita, com o
				// veredicto `inconclusivo`. Nenhum pedido saiu.
				return RelatorioDaQualificacaoQueNaoCoube(cfg)
			}
			return nil, err
		}
		// O conselheiro (AOS-528): as consultas — e a sonda dele — cabem no tecto do fornecedor
		// DELE; no mesmo fornecedor, somadas às do executor.
		if err := cfg.caberDoConselheiro(aEnviar); err != nil {
			return nil, err
		}
	}
	relogio := cfg.Relogio
	if relogio == nil {
		relogio = time.Now
	}
	parar := cfg.paragemDaQualificacao()

	var sonda *Sonda
	if cfg.Sondar {
		cfg.marcarFase(FaseDaSonda)
		s := cfg.No.Sondar(ctx)
		sonda = &s
		if s.Resultado != SondaOK {
			// A rota não responde: nenhum caso corre. Gastou-se, no máximo, um pedido.
			return construirRelatorio(cfg, relogio().UTC(), previstos, cfg.terminouDaQualificacao(terminouDaSonda(s)), sonda, nil, nil)
		}
		// A rota do conselheiro também se sonda, antes do primeiro caso (AOS-528).
		if cs := cfg.conselheiro(); cs != nil {
			doConselheiro := cs.Sondar(ctx)
			cfg.sondaDoConselheiro = &doConselheiro
			if doConselheiro.Resultado != SondaOK {
				return construirRelatorio(cfg, relogio().UTC(), previstos, terminouDaSonda(doConselheiro), sonda, nil, nil)
			}
		}
	}
	if err := cfg.registarQualificacao(); err != nil {
		return nil, err
	}
	cfg.marcarFase(FaseDaCorrida)
	obs, terminou := cicloDaCorrida(ctx, cfg, cfg.No, cfg.Plano, cfg.comParagemDoConselheiro(parar))
	terminou = cfg.terminouDaQualificacao(terminou)
	controlo := cfg.correrControlo(ctx, terminou, obs, parar)
	r, err := construirRelatorio(cfg, relogio().UTC(), previstos, terminou, sonda, obs, controlo)
	if err != nil || cfg.FormaNoFornecedor == nil {
		return r, err
	}
	// A forma lê-se DEPOIS do último pedido. Se não se conseguir ler, o relatório sai sem ela e
	// di-lo: a corrida não se perde por isso.
	if forma, ferr := cfg.FormaNoFornecedor(); ferr == nil {
		r.FormaNoFornecedor = forma
	} else {
		r.Limites = append(r.Limites, "A forma do que chegou ao provider falso nao se conseguiu ler no fim da corrida: o relatorio nao a tem.")
	}
	return r, nil
}

// cicloDaCorrida corre os passos de um plano num nó de ensaio e devolve as observações e o estado
// final. `parar`, se presente, é perguntado depois de cada run: uma causa não vazia pára a corrida.
func cicloDaCorrida(ctx context.Context, cfg CfgDaCorrida, noDeEnsaio *NoDeEnsaio, plano Plano, parar func() string) ([]Observacao, string) {
	var obs []Observacao
	terminou := TerminouCompleta
	vigia := novoVigiaDeErros()
	passos := plano.sequencia(cfg.Bateria)
ciclo:
	for i, ps := range passos {
		if i > 0 && cfg.Pausa > 0 {
			select {
			case <-ctx.Done():
			case <-time.After(cfg.Pausa):
			}
		}
		if ctx.Err() != nil {
			terminou = TerminouInterrompid
			break
		}
		saidas := map[string]string{}
		for _, no := range ps.caso.Nos {
			pedido := PedidoDeRun{Caso: ps.caso, No: no, Braco: ps.braco, Amostra: ps.amostra, MaxTurnos: plano.MaxTurnos, BracoDoConselheiro: ps.conselho}
			switch {
			case no.EntradaDe != "":
				anterior, ok := saidas[no.EntradaDe]
				if !ok {
					// O nó de que este depende não concluiu: não há entrada, e o nó não corre.
					continue
				}
				pedido.Entrada, pedido.OrigemEntrada = []byte(anterior), no.EntradaDe
			case no.EntradaDoc != "":
				doc, _ := cfg.Bateria.Documento(no.EntradaDoc)
				pedido.Entrada, pedido.OrigemEntrada = doc, "bateria"
			}
			for tentativa := 1; tentativa <= plano.Tentativas; tentativa++ {
				pedido.Tentativa = tentativa
				o, saida := noDeEnsaio.Correr(ctx, pedido)
				obs = append(obs, o)
				semDuzentos := 0
				for _, status := range o.HTTP {
					tipo := ""
					if status != 0 && status != http.StatusOK {
						if semDuzentos < len(o.TiposDeErro) {
							tipo = o.TiposDeErro[semDuzentos]
						}
						semDuzentos++
					}
					vigia.ver(status, tipo)
				}
				if causa := vigia.causa(cfg); causa != "" {
					terminou = causa
					break ciclo
				}
				if parar != nil {
					if causa := parar(); causa != "" {
						terminou = causa
						break ciclo
					}
				}
				switch o.Desfecho {
				case DesfechoTectoAtingido:
					terminou = TerminouTecto
					break ciclo
				case DesfechoContador:
					terminou = TerminouContador
					break ciclo
				case DesfechoCumprido:
					saidas[no.ID] = saida
				}
				if !desfechoRepetivel(o.Desfecho) {
					break
				}
				// A nova tentativa de um run que não pediu a tool leva o aviso do kernel
				// (AOS-506); a de uma resposta vazia não leva aviso (AOS-510).
				pedido.AvisoDeNovaTentativa = o.Desfecho == string(agentruntime.OutcomeContractNoCall)
			}
		}
		if cfg.Progresso != nil && ((i+1)%20 == 0 || i == len(passos)-1) {
			fmt.Fprintf(cfg.Progresso, "banco-ensaio: %d de %d passos\n", i+1, len(passos))
		}
	}
	return obs, terminou
}

// digestDaConfiguracao é o digest de tudo o que, além da bateria e da rota, decide o que a
// corrida mede: o plano, o texto das variantes, as versões publicadas e o que vier em Extra.
//
// `estado` é a composição do estado opaco do nó de ensaio (nil quando o perfil não devolve
// estado): muda o layout, e entra inteira no digest. Com nil o digest é o de antes do AOS-516.
func digestDaConfiguracao(p Plano, extra map[string]string, estado *ComposicaoDoEstado) string {
	doc := map[string]any{
		"plano": p, "alias": AliasDaRota, "extra": extra,
		"trocas_b": fmt.Sprint(trocasDoBracoB), "trocas_c": fmt.Sprint(trocasDoBracoC),
		"assembly": agentruntime.AssemblyVersion140,
	}
	if estado != nil {
		doc["assembly"], doc["estado_opaco"] = estado.Layout, estado
	}
	versoes := map[string]string{}
	for _, b := range Bracos() {
		versoes[string(b)] = VersaoPublicadaDoBraco(b)
	}
	doc["versoes"] = versoes
	var tools []string
	for _, t := range ToolsDoBanco() {
		tools = append(tools, digestDaTool(t))
	}
	doc["tools"] = tools
	cru, _ := json.Marshal(doc)
	soma := sha256.Sum256(cru)
	return "sha256:" + hex.EncodeToString(soma[:])
}
