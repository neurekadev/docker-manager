package migrations

func init() {
	// Stacks and services no longer have an icon of their own: the web shows
	// the stack type's icon (or the image of the template a stack was
	// created from) and one service icon. Clear the stored stack icons (the
	// column stays, unused, with its empty default) and drop "icon" from
	// the per-service metadata, removing entries left without any
	// metadata. Nothing to undo: the icons are gone on purpose.
	Migrations.MustRegister(
		Tx(Exec(
			`UPDATE stacks SET icon = '' WHERE icon != ''`,
			`UPDATE stacks SET service_meta = (
			   SELECT json_group_object(e.key,
			     CASE WHEN e.type = 'object' THEN json(json_remove(e.value, '$.icon')) ELSE e.value END)
			   FROM json_each(stacks.service_meta) AS e
			   WHERE CASE WHEN e.type = 'object' THEN json_remove(e.value, '$.icon') != '{}' ELSE 1 END)
			 WHERE json_valid(service_meta) AND json_type(service_meta) = 'object'
			   AND EXISTS (
			     SELECT 1 FROM json_each(stacks.service_meta) AS e
			     WHERE e.type = 'object' AND json_type(e.value, '$.icon') IS NOT NULL)`,
		)),
		Tx(Exec()),
	)
}
