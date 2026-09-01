package main

// RENOVAÇÃO DO TOKEN DA CUSTÓDIA DA KEK (AOS-249, achado F6) — vive no LOOP DE SERVIÇO, no molde
// EXACTO do varrimento de aprovações (approval_sweeper.go) e do varrimento de deadlines
// (deadline_sweeper.go): um ticker que termina com o `sweepStop` do Shutdown.
//
// O PROBLEMA QUE FECHA. O token do Vault era lido UMA vez, no arranque, e nunca mais tocado —
// enquanto o README do operador recomenda, para produção, um token de CURTA DURAÇÃO
// (AppRole/Kubernetes-auth). Seguir a recomendação matava a custódia: passado o TTL, cada
// `WrapDEK` e cada crypto-shred passava a levar 403 do Vault e o nó não dava por isso, porque a
// única sonda que existia (`/v1/sys/seal-status`) é NÃO-AUTENTICADA e continuava a responder
// `sealed:false`. Um /readyz verde sobre uma via GDPR partida.
//
// AS DUAS METADES, e porque são duas. Este laço é a metade que EVITA a expiração (re-lê o
// ficheiro montado, mede a validade e renova a lease antes da margem). A sonda autenticada em
// [vaultKeyVault.ready] é a metade que a DENUNCIA: se este laço falhar — Vault em baixo, política
// que já não permite renovar, agente externo que deixou de rodar o ficheiro — o /readyz fica
// VERMELHO ANTES da expiração, e o orquestrador drena o nó em vez de lhe mandar tráfego que ele
// já não consegue cifrar. Uma metade sem a outra volta a ser uma morte silenciosa: a renovação
// sozinha falha calada, e a sonda sozinha só transforma a morte silenciosa numa morte anunciada.
//
// NUNCA se loga o VALOR do token — só o CAMINHO do ficheiro e o veredicto.
//
// REGIME DE POSSE (AOS-283): CREDENCIAL POR PROCESSO — corre em TODAS as réplicas, sem
// lease, e um lease aqui seria ACTIVAMENTE ERRADO. O que este laço mantém vivo é o token
// DESTE processo; sob posse exclusiva, as réplicas não-líderes deixariam o seu token
// expirar e a custódia da KEK morria nelas — a falha exacta que este ficheiro fecha. Não
// há estado partilhado: não há nada que duas réplicas possam duplicar.

import (
	"context"
	"time"
)

// DefaultVaultTokenRenewInterval é o período de manutenção por omissão. Uma fracção folgada da
// margem de prontidão ([DefaultVaultTokenMinTTL]), pelo mesmo raciocínio do heartbeat do lease
// (TTL/3): renova-se várias vezes dentro da janela em que ainda há margem, para que uma falha
// isolada de rede não chegue a mover a sonda.
const DefaultVaultTokenRenewInterval = 1 * time.Minute

// vaultTokenRefresher é a custódia que sabe manter a sua própria credencial. O [vaultKeyVault]
// implementa-o; o vault in-memory de referência NÃO (não há token nenhum) — nesse caso o laço
// nem arranca, comportamento inalterado. Molde de [readinessProber].
type vaultTokenRefresher interface {
	refreshToken(context.Context) error
}

// WithVaultTokenRenewInterval sobrepõe o período de manutenção do token da custódia. <= 0
// DESLIGA o laço (usado em testes que conduzem a manutenção à mão via
// [NodeService.RefreshVaultTokenNow]). Arma a flag Set — sem ela o "explicitamente 0" seria
// indistinguível de "não configurado" e o default ganharia.
func WithVaultTokenRenewInterval(d time.Duration) NodeServiceOption {
	return func(c *nodeServiceConfig) {
		c.vaultTokenRenewInterval = d
		c.vaultTokenRenewIntervalSet = true
	}
}

// renewVaultToken é o laço periódico. Termina quando stop fecha (shutdown do serviço).
func (s *NodeService) renewVaultToken(stop <-chan struct{}) {
	if s.vaultTokenRenewInterval <= 0 || s.node == nil {
		return
	}
	if _, ok := s.node.DSARVault.(vaultTokenRefresher); !ok {
		return // custódia sem token (in-memory): nada a manter
	}
	t := time.NewTicker(s.vaultTokenRenewInterval)
	defer t.Stop()
	for {
		select {
		case <-stop:
			return
		case <-t.C:
			s.renewVaultTokenOnce(context.Background())
		}
	}
}

// renewVaultTokenOnce corre UMA manutenção. É best-effort e idempotente: um erro é REGISTADO
// (nunca silenciado) e re-tentado no tick seguinte — falhar aqui não interrompe o serviço porque
// não é este laço que decide a prontidão; é a sonda autenticada que o faz, com a margem.
func (s *NodeService) renewVaultTokenOnce(ctx context.Context) {
	r, ok := s.node.DSARVault.(vaultTokenRefresher)
	if !ok {
		return
	}
	// Tecto por-tentativa: a manutenção não pode ficar pendurada num Vault lento e segurar o
	// ticker (o próximo tick chegaria com o anterior ainda a correr).
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	if err := r.refreshToken(ctx); err != nil {
		// O erro traz caminho e veredicto — NUNCA o token.
		s.log("custodia da KEK (AOS-249): manutencao do token do Vault FALHOU (re-tenta no proximo tick; o /readyz fica VERMELHO antes da expiracao): %v", err)
		return
	}
	// POSTURA OPACA: a credencial serve mas não se deixa medir. Sai UMA vez — a versão anterior
	// repetia o alarme a cada tick e ninguém o leu em 43 repetições.
	if a, ok := r.(interface{ avisoDeOpacidade() string }); ok {
		if msg := a.avisoDeOpacidade(); msg != "" {
			s.log("%s", msg)
		}
	}
}

// RefreshVaultTokenNow corre UMA manutenção imediatamente. Existe para os testes a conduzirem de
// forma determinista, sem esperar pelo ticker (molde de [NodeService.SweepApprovalsNow]).
func (s *NodeService) RefreshVaultTokenNow(ctx context.Context) {
	if s.node == nil {
		return
	}
	s.renewVaultTokenOnce(ctx)
}
