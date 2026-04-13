package handlers

import (
	"ByteBucket/internal/storage"
	"fmt"
	"net/http"

	"github.com/gin-gonic/gin"
)

// ACLRuleRequest represents an ACL rule in the admin API.
type ACLRuleRequest struct {
	Effect  string   `json:"effect" binding:"required"`  // "Allow" or "Deny"
	Buckets []string `json:"buckets" binding:"required"` // e.g. ["*"] or specific bucket names
	Actions []string `json:"actions" binding:"required"` // e.g. ["*"] or specific actions
}

// CreateUserRequest represents the request payload for creating a new user.
type CreateUserRequest struct {
	ACL []ACLRuleRequest `json:"acl" binding:"required"` // ACL rules for the user
}

// CreateUserHandler creates a new user with generated credentials and ACL rules.
// The user ID is the generated accessKeyID.
func CreateUserHandler(c *gin.Context) {
	var req CreateUserRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request: missing ACL rules"})
		return
	}

	// Convert ACL rules from request to storage ACLRule.
	var aclRules []storage.ACLRule
	for _, rule := range req.ACL {
		aclRules = append(aclRules, storage.ACLRule{
			Effect:  rule.Effect,
			Buckets: rule.Buckets,
			Actions: rule.Actions,
		})
	}

	user, err := storage.CreateUserWithACL(aclRules)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("Error creating user: %v", err)})
		return
	}

	c.JSON(http.StatusCreated, gin.H{
		"accessKeyID":     user.AccessKeyID,
		"secretAccessKey": user.SecretAccessKey, // Returned only on creation
		"acl":             user.ACL,
	})
}

// UpdateUserHandler updates an existing user's ACL.
// The accessKeyID is used as the user identifier.
func UpdateUserHandler(c *gin.Context) {
	accessKeyID := c.Param("accessKeyID")
	var req CreateUserRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request: missing ACL rules"})
		return
	}

	var aclRules []storage.ACLRule
	for _, rule := range req.ACL {
		aclRules = append(aclRules, storage.ACLRule{
			Effect:  rule.Effect,
			Buckets: rule.Buckets,
			Actions: rule.Actions,
		})
	}

	if err := storage.UpdateUserACL(accessKeyID, aclRules); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("Error updating user: %v", err)})
		return
	}
	c.Status(http.StatusOK)
}

// ListUsersHandler returns a list of users without exposing their encrypted secret.
func ListUsersHandler(c *gin.Context) {
	users, err := storage.ListUsers()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Error listing users"})
		return
	}
	var result []gin.H
	for _, u := range users {
		result = append(result, gin.H{
			"accessKeyID": u.AccessKeyID,
			"acl":         u.ACL,
		})
	}
	c.JSON(http.StatusOK, result)
}

// DeleteUserHandler deletes a user using their accessKeyID.
func DeleteUserHandler(c *gin.Context) {
	accessKeyID := c.Param("accessKeyID")
	if err := storage.DeleteUser(accessKeyID); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("Error deleting user: %v", err)})
		return
	}
	c.Status(http.StatusNoContent)
}

