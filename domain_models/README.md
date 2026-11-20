# domain_models

The platform's domain data models, as Pydantic models. They are the source
of truth: `schemas/domain.schema.json` is generated from them, and the types
of every other language (Go in Sortie and the worker daemon, TypeScript in
the workbench frontend) are generated from that schema.

What each model means, and the rules about versions and IDs, are in
[`docs/architecture/models.md`](../docs/architecture/models.md).

```sh
make test      # the tests, including the check that schemas/ is current
make schemas   # regenerate schemas/ after changing a model
```

New IDs come from `domain_models.ids.new_id`, never from anywhere else.
