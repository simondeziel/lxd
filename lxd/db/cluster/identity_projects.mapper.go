//go:build linux && cgo && !agent

package cluster

// The code below was generated by lxd-generate - DO NOT EDIT!

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/canonical/lxd/lxd/db/query"
	"github.com/canonical/lxd/shared/api"
)

var _ = api.ServerEnvironment{}

var identityProjectObjects = RegisterStmt(`
SELECT identities_projects.identity_id, identities_projects.project_id
  FROM identities_projects
  ORDER BY identities_projects.identity_id
`)

var identityProjectObjectsByIdentityID = RegisterStmt(`
SELECT identities_projects.identity_id, identities_projects.project_id
  FROM identities_projects
  WHERE ( identities_projects.identity_id = ? )
  ORDER BY identities_projects.identity_id
`)

var identityProjectCreate = RegisterStmt(`
INSERT INTO identities_projects (identity_id, project_id)
  VALUES (?, ?)
`)

var identityProjectDeleteByIdentityID = RegisterStmt(`
DELETE FROM identities_projects WHERE identity_id = ?
`)

// identityProjectColumns returns a string of column names to be used with a SELECT statement for the entity.
// Use this function when building statements to retrieve database entries matching the IdentityProject entity.
func identityProjectColumns() string {
	return "identity_projects.identity_id, identity_projects.project_id"
}

// getIdentityProjects can be used to run handwritten sql.Stmts to return a slice of objects.
func getIdentityProjects(ctx context.Context, stmt *sql.Stmt, args ...any) ([]IdentityProject, error) {
	objects := make([]IdentityProject, 0)

	dest := func(scan func(dest ...any) error) error {
		i := IdentityProject{}
		err := scan(&i.IdentityID, &i.ProjectID)
		if err != nil {
			return err
		}

		objects = append(objects, i)

		return nil
	}

	err := query.SelectObjects(ctx, stmt, dest, args...)
	if err != nil {
		return nil, fmt.Errorf("Failed to fetch from \"identity_projects\" table: %w", err)
	}

	return objects, nil
}

// getIdentityProjectsRaw can be used to run handwritten query strings to return a slice of objects.
func getIdentityProjectsRaw(ctx context.Context, tx *sql.Tx, sql string, args ...any) ([]IdentityProject, error) {
	objects := make([]IdentityProject, 0)

	dest := func(scan func(dest ...any) error) error {
		i := IdentityProject{}
		err := scan(&i.IdentityID, &i.ProjectID)
		if err != nil {
			return err
		}

		objects = append(objects, i)

		return nil
	}

	err := query.Scan(ctx, tx, sql, dest, args...)
	if err != nil {
		return nil, fmt.Errorf("Failed to fetch from \"identity_projects\" table: %w", err)
	}

	return objects, nil
}

// GetIdentityProjects returns all available Projects for the Identity.
// generator: identity_project GetMany
func GetIdentityProjects(ctx context.Context, tx *sql.Tx, identityID int) ([]Project, error) {
	var err error

	// Result slice.
	objects := make([]IdentityProject, 0)

	sqlStmt, err := Stmt(tx, identityProjectObjectsByIdentityID)
	if err != nil {
		return nil, fmt.Errorf("Failed to get \"identityProjectObjectsByIdentityID\" prepared statement: %w", err)
	}

	args := []any{identityID}

	// Select.
	objects, err = getIdentityProjects(ctx, sqlStmt, args...)
	if err != nil {
		return nil, fmt.Errorf("Failed to fetch from \"identity_projects\" table: %w", err)
	}

	result := make([]Project, len(objects))
	for i, object := range objects {
		project, err := GetProjects(ctx, tx, ProjectFilter{ID: &object.ProjectID})
		if err != nil {
			return nil, err
		}

		result[i] = project[0]
	}

	return result, nil
}

// DeleteIdentityProjects deletes the identity_project matching the given key parameters.
// generator: identity_project DeleteMany
func DeleteIdentityProjects(ctx context.Context, tx *sql.Tx, identityID int) error {
	stmt, err := Stmt(tx, identityProjectDeleteByIdentityID)
	if err != nil {
		return fmt.Errorf("Failed to get \"identityProjectDeleteByIdentityID\" prepared statement: %w", err)
	}

	result, err := stmt.Exec(int(identityID))
	if err != nil {
		return fmt.Errorf("Delete \"identity_projects\" entry failed: %w", err)
	}

	_, err = result.RowsAffected()
	if err != nil {
		return fmt.Errorf("Fetch affected rows: %w", err)
	}

	return nil
}

// CreateIdentityProjects adds a new identity_project to the database.
// generator: identity_project Create
func CreateIdentityProjects(ctx context.Context, tx *sql.Tx, objects []IdentityProject) error {
	for _, object := range objects {
		args := make([]any, 2)

		// Populate the statement arguments.
		args[0] = object.IdentityID
		args[1] = object.ProjectID

		// Prepared statement to use.
		stmt, err := Stmt(tx, identityProjectCreate)
		if err != nil {
			return fmt.Errorf("Failed to get \"identityProjectCreate\" prepared statement: %w", err)
		}

		// Execute the statement.
		_, err = stmt.Exec(args...)
		if err != nil {
			return fmt.Errorf("Failed to create \"identity_projects\" entry: %w", err)
		}

	}

	return nil
}

// UpdateIdentityProjects updates the identity_project matching the given key parameters.
// generator: identity_project Update
func UpdateIdentityProjects(ctx context.Context, tx *sql.Tx, identityID int, projectNames []string) error {
	// Delete current entry.
	err := DeleteIdentityProjects(ctx, tx, identityID)
	if err != nil {
		return err
	}

	// Get new entry IDs.
	identityProjects := make([]IdentityProject, 0, len(projectNames))
	for _, entry := range projectNames {
		refID, err := GetProjectID(ctx, tx, entry)
		if err != nil {
			return err
		}

		identityProjects = append(identityProjects, IdentityProject{IdentityID: identityID, ProjectID: int(refID)})
	}

	err = CreateIdentityProjects(ctx, tx, identityProjects)
	if err != nil {
		return err
	}

	return nil
}
