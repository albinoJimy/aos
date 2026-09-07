package main

import (
	"testing"
)

// TestAOS363_PrivilegedCapsFromEnv prova a superfície de configuração AOS_PRIVILEGED_CAPS
// — e que ela alimenta MESMO Config.Privileged, sem o que o TaintGate era inalcançável pelo
// binário (o achado central de analises/13 §2.1).
//
//   - AUSENTE ⇒ Config.Privileged == nil (perna retro-compatível, TaintGate inerte);
//   - DEFINIDA-MAS-VAZIA (ou só separadores) ⇒ nil também: é como o idioma da casa
//     "desconfigura" uma variável, e um `${VAR:-}` de docker-compose faz o mesmo. Tratá-la
//     como erro quebraria esses deployments — o oposto da retro-compatibilidade exigida;
//   - COM capabilities ⇒ Config.Privileged não-nil e classifica cada capability listada como
//     privilegiada, e uma não-listada como não-privilegiada (os dois sentidos).
//
// Controlo negativo embutido: os subtestes "ausente"/"vazia" e o "com caps" partilham tudo
// excepto o valor da variável — se a leitura de AOS_PRIVILEGED_CAPS fosse neutralizada, o
// subteste "com caps" veria Privileged==nil e avermelharia.
func TestAOS363_PrivilegedCapsFromEnv(t *testing.T) {
	// Higiene mínima para nodeConfigFromEnv arrancar sem herdar a máquina. Só o campo em
	// teste varia; os restantes obrigatórios têm defaults de referência.
	prep := func(t *testing.T) {
		t.Setenv("AOS_HUMANS", "operator")
	}

	t.Run("ausente ⇒ Privileged nil (retro-compat)", func(t *testing.T) {
		prep(t)
		// NÃO define AOS_PRIVILEGED_CAPS.
		cfg, err := nodeConfigFromEnv()
		if err != nil {
			t.Fatalf("nodeConfigFromEnv: %v", err)
		}
		if cfg.Privileged != nil {
			t.Fatal("AOS_PRIVILEGED_CAPS ausente devia deixar Config.Privileged nil (perna retro-compatível)")
		}
	})

	t.Run("com capabilities ⇒ conjunto classifica os dois sentidos", func(t *testing.T) {
		prep(t)
		t.Setenv("AOS_PRIVILEGED_CAPS", "cap:fs.write, cap:net.connect")
		cfg, err := nodeConfigFromEnv()
		if err != nil {
			t.Fatalf("nodeConfigFromEnv: %v", err)
		}
		if cfg.Privileged == nil {
			t.Fatal("AOS_PRIVILEGED_CAPS com capabilities devia preencher Config.Privileged (a leitura foi neutralizada?)")
		}
		if !cfg.Privileged.IsPrivileged("cap:fs.write") {
			t.Error("cap:fs.write listada mas IsPrivileged==false")
		}
		if !cfg.Privileged.IsPrivileged("cap:net.connect") {
			t.Error("cap:net.connect listada (com espaço à frente) mas IsPrivileged==false — o trim de splitCSV falhou")
		}
		if cfg.Privileged.IsPrivileged("cap:fs.read") {
			t.Error("cap:fs.read NÃO listada mas IsPrivileged==true — o conjunto não é o esperado")
		}
	})

	t.Run("definida-mas-vazia ⇒ nil (retro-compat, como o idioma da casa desconfigura)", func(t *testing.T) {
		prep(t)
		t.Setenv("AOS_PRIVILEGED_CAPS", "")
		cfg, err := nodeConfigFromEnv()
		if err != nil {
			t.Fatalf("AOS_PRIVILEGED_CAPS=\"\" NÃO pode abortar — é como o helper de teste e um docker-compose ${VAR:-} desconfiguram: %v", err)
		}
		if cfg.Privileged != nil {
			t.Fatal("AOS_PRIVILEGED_CAPS=\"\" devia deixar Config.Privileged nil (inerte, retro-compat)")
		}
	})

	t.Run("só vírgulas/espaços ⇒ nil (inerte)", func(t *testing.T) {
		prep(t)
		t.Setenv("AOS_PRIVILEGED_CAPS", " , , ")
		cfg, err := nodeConfigFromEnv()
		if err != nil {
			t.Fatalf("AOS_PRIVILEGED_CAPS só com separadores não pode abortar: %v", err)
		}
		if cfg.Privileged != nil {
			t.Fatal("AOS_PRIVILEGED_CAPS só com separadores devia deixar Config.Privileged nil (inerte)")
		}
	})
}
