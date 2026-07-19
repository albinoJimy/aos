package sandbox

import (
	"context"
	"math"
	"time"
)

// autoscale.go — AOS-103: dimensionamento do pool de microVMs DERIVADO DO HEADROOM.
//
// O critério de aceitação central do AOS-103 é "o tamanho do pool é derivado do
// headroom (não uma constante) e ajusta-se à carga". O pool base (AOS-065) já
// pré-aquece, reserva e repõe VMs, mas com warmN/maxN CONSTANTES. Este ficheiro
// acrescenta, de forma ADITIVA e ZERO-DEP, a peça em falta: uma fonte de headroom
// (porta), uma fórmula de derivação (pura/determinista) e um laço que reajusta o
// pool via [Pool.Resize].
//
// # Fronteira de composição — porta na sandbox, adaptador no ápice
//
// A fonte de verdade do headroom é o ADMISSION CONTROL global do escalonador
// (AOS-027/028, `control-plane/scheduler`): o mesmo token-bucket distribuído sobre o
// TPM/RPM real que deriva o `max_spawn`. A sandbox é um módulo de SUBSTRATO e NÃO
// pode importar o plano de controlo (inverteria a direcção de dependência e criaria
// ciclo). Por isso a porta [HeadroomSource] é definida AQUI em termos da sandbox
// (unidades abstractas de capacidade), e o ADAPTADOR que traduz o
// `scheduler.HeadroomSnapshot` (tokens/requests + custo por VM) para estas unidades
// vive no COMPOSITION ROOT (ápice, `packages/integration`), a jusante de ambos —
// exactamente o padrão "porta no pilar + adaptador no ápice" já usado no egress da
// sandbox (AOS-067) e na soberania do Event Store (AOS-100).
//
// Sem esse adaptador ligado (a via de referência/testes injecta uma fonte
// determinista), o pool comporta-se como em AOS-065 com os alvos iniciais fixos: o
// autoscaling fica DORMENTE por omissão (retro-compatível), não altera o pool base.
// A ligação da fonte real de headroom em produção é wiring de ápice — o mecanismo
// (autoscaler + Resize + tecto absoluto) está entregue e provado aqui.

// DefaultAutoscaleInterval é o intervalo default do laço [Autoscaler.Run] quando o
// chamador não fornece um (<=0). A cadência real em produção é uma decisão
// operacional (equilíbrio entre reactividade à carga e custo de restore das
// reposições); este default é conservador.
const DefaultAutoscaleInterval = 5 * time.Second

// Headroom é o headroom do provider reportado à sandbox em unidades ABSTRACTAS de
// capacidade. O adaptador de ápice reduz a projecção do escalonador (tokens/requests
// disponíveis, com o custo por sub-agente/VM) a este escalar único — a sandbox não
// conhece TPM/RPM nem tenants. Available é o que RESTA agora; Limit é o tecto (>=
// Available), disponível para políticas de dimensionamento por fracção.
type Headroom struct {
	// Available é a capacidade disponível AGORA (nunca negativa por contrato; o
	// sizer volta a fixá-la a >=0 por defesa).
	Available int64
	// Limit é o tecto de capacidade (informativo; >= Available). Não é usado pelo
	// [DefaultPoolSizer], mas está disponível para sizers por fracção do tecto.
	Limit int64
}

// HeadroomSource é a PORTA (definida em termos da sandbox, ZERO-DEP) pela qual o
// [Autoscaler] observa o headroom disponível. *NÃO* é implementada aqui pelo
// escalonador — o adaptador que liga `scheduler.Admission.Headroom`/
// `SpawnCoordinator.MaxSpawn` a esta porta vive no composition root (ver o cabeçalho
// deste ficheiro). Nos testes há uma fonte determinista de referência.
type HeadroomSource interface {
	// Headroom devolve o headroom corrente. Um erro NÃO deve fazer o pool crescer —
	// o [Autoscaler.Run] mantém o último tamanho conhecido e volta a tentar.
	Headroom(ctx context.Context) (Headroom, error)
}

// PoolSize é o par de alvos derivados do headroom que o [Autoscaler] aplica ao pool:
// Warm (VMs pré-aquecidas) e Max (tecto de VMs vivas). Warm <= Max por construção do
// sizer; ambos são novamente fixados a [0, absMax] por [Pool.Resize].
type PoolSize struct {
	Warm int
	Max  int
}

// PoolSizer deriva [PoolSize] a partir do [Headroom]. É PURO e DETERMINISTA (sem
// relógio nem estado): o mesmo headroom produz sempre o mesmo tamanho — reproduzível
// e testável. Injectável para políticas alternativas (ex.: reservar headroom para
// picos, dimensionar por fracção do tecto).
type PoolSizer func(Headroom) PoolSize

// DefaultPoolSizer é a fórmula de referência de derivação do tamanho do pool a partir
// do headroom, análoga a `scheduler.deriveMaxSpawn` (AOS-028):
//
//	slots = headroom_disponível / custo_por_VM   (fixado ao tecto absoluto)
//	max   = slots
//	warm  = ceil(slots · warmFraction)           (fixado a <= max)
//
// Propriedades (verificadas em teste, à imagem de deriveMaxSpawn):
//   - NÃO é constante: varia com o headroom;
//   - ZERO sob headroom nulo (slots=0 ⇒ warm=max=0 ⇒ pool fail-closed, degrada para
//     AOS-107 em vez de servir para lá do headroom);
//   - MONÓTONA não-decrescente no headroom: h1 <= h2 ⇒ size(h1) <= size(h2);
//   - custo_por_VM <= 0 é normalizado para 1 (nunca divisão por zero / pool ilimitado);
//   - o tecto ABSOLUTO domina sempre (um headroom/adaptador errado nunca faz o pool
//     crescer para lá do limite físico).
//
// warmFraction ∈ (0,1] escolhe que fracção do headroom fica PRÉ-AQUECIDA (1.0 =
// pré-aquecer todo o headroom, o mais conservador para o cold-start; < 1.0 poupa
// restores de reposição à custa de expandir sob pico). Fora de gama cai para 1.0.
func DefaultPoolSizer(costPerVMUnits int64, absMax int, warmFraction float64) PoolSizer {
	if costPerVMUnits < 1 {
		costPerVMUnits = 1
	}
	if absMax < 0 {
		absMax = 0
	}
	if warmFraction <= 0 || warmFraction > 1 {
		warmFraction = 1
	}
	return func(h Headroom) PoolSize {
		avail := h.Available
		if avail < 0 {
			avail = 0
		}
		slots := int(avail / costPerVMUnits)
		if slots > absMax {
			slots = absMax
		}
		max := slots
		warm := int(math.Ceil(float64(slots) * warmFraction))
		if warm > max {
			warm = max
		}
		if warm < 0 {
			warm = 0
		}
		return PoolSize{Warm: warm, Max: max}
	}
}

// Autoscaler reajusta um [Pool] ao headroom observado por uma [HeadroomSource],
// usando um [PoolSizer]. Compõe (não reimplementa) o pool: cada avaliação chama
// [Pool.Resize], que cresce/encolhe de forma segura sob concorrência e nunca
// ultrapassa o tecto absoluto. Construir com [NewAutoscaler].
type Autoscaler struct {
	pool   *Pool
	source HeadroomSource
	sizer  PoolSizer
}

// NewAutoscaler constrói o autoscaler. Todas as dependências são OBRIGATÓRIAS —
// fail-closed sem pool, sem fonte de headroom ou sem sizer.
func NewAutoscaler(pool *Pool, source HeadroomSource, sizer PoolSizer) (*Autoscaler, error) {
	if pool == nil {
		return nil, ErrNilPool
	}
	if source == nil {
		return nil, ErrNilHeadroomSource
	}
	if sizer == nil {
		return nil, ErrNilPoolSizer
	}
	return &Autoscaler{pool: pool, source: source, sizer: sizer}, nil
}

// Tick lê o headroom UMA vez, deriva o tamanho e reajusta o pool. É a unidade
// DETERMINISTA do autoscaling (testável sem relógio real). Propaga o erro de leitura
// de headroom SEM tocar no pool — nunca redimensiona a partir de um headroom que não
// pôde observar. Devolve o [PoolSize] aplicado.
func (a *Autoscaler) Tick(ctx context.Context) (PoolSize, error) {
	if err := ctx.Err(); err != nil {
		return PoolSize{}, err
	}
	h, err := a.source.Headroom(ctx)
	if err != nil {
		return PoolSize{}, err
	}
	sz := a.sizer(h)
	a.pool.Resize(sz.Warm, sz.Max)
	return sz, nil
}

// Run corre o laço de autoscaling até o ctx terminar. Avalia imediatamente e depois a
// cada `interval` (<=0 herda [DefaultAutoscaleInterval]). Um erro de leitura de
// headroom NÃO aborta o laço nem redimensiona o pool: mantém-se o último tamanho
// conhecido (a fonte de verdade do enforcement é o admission control do escalonador;
// o dimensionamento do pool é uma optimização) e re-tenta no tick seguinte. Devolve
// o motivo de cancelamento do ctx.
func (a *Autoscaler) Run(ctx context.Context, interval time.Duration) error {
	if interval <= 0 {
		interval = DefaultAutoscaleInterval
	}
	// Avaliação imediata (não espera o primeiro tick para dimensionar ao headroom).
	_, _ = a.Tick(ctx)

	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.C:
			// Erro de headroom é transitório: ignora-o e mantém o último tamanho.
			_, _ = a.Tick(ctx)
		}
	}
}
