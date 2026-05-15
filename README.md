# Terraform-Refactoring-Blocks-Auto-Cleaner

`tfrbac` removes Terraform refactoring blocks: `import`, `moved`, and `removed`.

## Install

```bash
brew install dev-hato/tap/tfrbac
```

Or download a binary from [release](https://github.com/dev-hato/tfrbac/releases/latest).

## Usage

Run it from a Terraform project, or pass one or more directories or `.tf` files.

```bash
tfrbac [flags] [PATH...]
```

Examples:

```bash
tfrbac
tfrbac ./infra ./modules/network
tfrbac ./main.tf
tfrbac --dry-run ./infra
tfrbac --check ./infra
tfrbac --exclude .cache ./infra
```

Flags:

- `--check`: print files that would change and exit with status `1` if any are found
- `--dry-run`: print files that would change without writing them
- `--exclude`: skip a directory name while walking. Repeatable
- `--version`: print the binary version

## Safety

By default, `tfrbac` skips these directories while walking a directory target:

- `.git`
- `.terraform`
- `.terragrunt-cache`

`--check` is useful in CI, and `--dry-run` is useful before a bulk cleanup.

`tfrbac` rewrites Terraform files in place. Run it after you have already applied the refactoring block change you intend to keep, and review the resulting diff before commit.

## Before / After

Before:

```hcl
resource "aws_instance" "example" {
}

moved {
  from = aws_instance.old_name
  to   = aws_instance.example
}
```

After:

```hcl
resource "aws_instance" "example" {
}
```
