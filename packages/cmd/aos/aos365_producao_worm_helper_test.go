package main

import (
	"os"
	"path/filepath"
	"testing"
)

// fixarSubstratoDuravelDeProducao satisfaz, de uma vez, o TRILHO WORM durável que AOS-365 passou a
// exigir em produção — e a cascata de guardas que ele arrasta atrás de si — para que um helper de
// família que compõe uma produção «quase completa» continue a medir a SUA coluna e não a primeira
// coluna de durabilidade que ficou por definir.
//
// A CASCATA são DUAS portas, e é EXACTAMENTE o que a mensagem de erro de AOS-365 manda definir —
// nem mais uma variável:
//   - WORM durável           (AOS-365, [ErrProductionNeedsDurableWORM]) → AOS_WORM_PATH
//   - KEK tão durável quanto (AOS-215, [ErrProductionNeedsDurableKEK])  → AOS_DSAR_VAULT_ADDR (+token)
//
// A guarda de confirmação de destruição (AOS-328, [ErrProductionNeedsShredConfirmation]) NÃO se
// acrescenta: a custódia que `AOS_DSAR_VAULT_ADDR` compõe é um `*vaultKeyVault`, que IMPLEMENTA a
// porta de confirmação (`shredConfirmed`, detectada por `confirmadorDeShredDe`), pelo que a guarda
// passa por si. É por isso que este helper NÃO define AOS_DSAR_VAULT_DESTROY_UNCONDITIONAL — fazê-lo
// satisfaria a AOS-328 por uma segunda via redundante e, pior, ensinaria uma configuração que
// SUPRIME o aviso AOS-322 numa custódia que genuinamente saiba confirmar. O operador que segue a
// mensagem de erro (as duas variáveis, e só elas) arranca — que é o que CA-5 promete.
//
// É o mesmo alargamento que AOS-300 fez quando tornou o Event Store durável obrigatório: as
// fixtures de arranque de produção ganharam AOS_EVENTSTORE_PATH. O Event Store fica a cargo de cada
// helper (que já o monta no seu próprio tempdir); aqui fecha-se só o eixo do WORM e a KEK que ele
// arrasta.
//
// O WORM aponta para um ficheiro NOVO num tempdir do teste — o `OpenFileStore` cria-o e sela nele a
// génese, sem I/O de rede. O vault é o de REFERÊNCIA de teste: `https://` (o esquema é validado
// fail-closed desde AOS-249, o token nunca viaja em claro) e um token em ficheiro montado; o
// arranque não o disca (o `restoreShredPending` só toca no vault se a cadeia tiver eventos
// `governance.dsar`, e uma cadeia recém-selada não tem nenhum).
func fixarSubstratoDuravelDeProducao(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("AOS_WORM_PATH", filepath.Join(dir, "worm.wal"))
	t.Setenv("AOS_DSAR_VAULT_ADDR", "https://vault:8200")
	tok := filepath.Join(dir, "vault-token")
	if err := os.WriteFile(tok, []byte("dev-root"), 0o600); err != nil {
		t.Fatalf("escrever token de vault de teste: %v", err)
	}
	t.Setenv("AOS_DSAR_VAULT_TOKEN_PATH", tok)
}
