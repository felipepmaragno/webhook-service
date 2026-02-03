---
description: Instalar uma skill de um repositório GitHub ou local
---

# Workflow: Instalar Skill

## Uso
Quando o usuário pedir para instalar uma skill, siga estes passos:

## 1. Identificar a fonte
- Se for URL do GitHub: clonar o repositório ou baixar a pasta específica
- Se for local: copiar a pasta

## 2. Validar estrutura
A skill DEVE ter:
- Pasta com nome em kebab-case (ex: `code-review`)
- Arquivo `SKILL.md` com frontmatter YAML contendo `name` e `description`

## 3. Escolher escopo
- **Workspace** (projeto atual): `.windsurf/skills/<skill-name>/`
- **Global** (todos os projetos): `~/.codeium/windsurf/skills/<skill-name>/`

## 4. Instalar
```bash
# Para skill global (exemplo com git sparse-checkout)
cd ~/.codeium/windsurf/skills
git clone --depth 1 --filter=blob:none --sparse <repo-url> temp-skill
cd temp-skill
git sparse-checkout set <path-to-skill>
mv <path-to-skill>/* ../<skill-name>/
cd .. && rm -rf temp-skill

# Ou simplesmente copiar a pasta se já tiver localmente
cp -r /path/to/skill ~/.codeium/windsurf/skills/
```

## 5. Verificar
- Confirmar que `SKILL.md` existe
- Testar invocando `@skill-name` no Cascade

## Fontes conhecidas de Skills
- `github.com/anthropics/skills` - Skills oficiais da Anthropic
- `github.com/agentskills/agentskills` - Especificação e exemplos
- `skillsmp.com` - Marketplace comunitário
