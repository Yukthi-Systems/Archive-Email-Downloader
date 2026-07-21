/*
 * Copyright (C) 2026 Yukthi Systems Private Limited
 *
 * This program is free software: you can redistribute it and/or modify
 * it under the terms of the GNU General Public License version 3
 * as published by the Free Software Foundation.
 *
 * This program is distributed in the hope  that it will be useful,
 * but WITHOUT ANY WARRANTY; without even the implied warranty of
 * MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
 * GNU General Public License for more details.
 *
 * You should have received a copy of the GNU General Public License
 * version 3 along with this program. If not, see
 * <https://www.gnu.org/licenses/>.
 */

package postscript

import (
	"archive-email-downloader/internal/db"
	"archive-email-downloader/internal/logger"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/smtp"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// SMTPConfig holds connection details for the outbound mail server.
// All fields are required when --mail-to is provided.
type SMTPConfig struct {
	Host     string // e.g. "smtp.example.com"
	Port     string // e.g. "587"
	Username string
	Password string
	From     string // e.g. "no-reply@example.com"
	FromName string // e.g. "Archive Export Service"
}

func RunPostActions(outputDir, sqlitePath, jobID, mailTo string, smtp SMTPConfig, archiveBaseURL string, appLog *logger.Logger) error {
	jobDir := filepath.Join(outputDir, jobID)

	sqlDest := filepath.Join(jobDir, "sql-lite.db")
	if err := copyFile(sqlitePath, sqlDest); err != nil {
		return fmt.Errorf("failed to copy database: %w", err)
	}
	appLog.GeneralLog.Info("Database copied to job directory", "job", jobID)

	var completed, failed, total int64

	repo, err := db.NewRepository(sqlitePath)
	if err == nil {
		defer repo.Close()
		completed, failed, total, err = repo.GetProgressStats()
		if err != nil {
			appLog.ErrorLog.Error("Failed to get progress stats", "error", err)
		}
	}

	zipSize := calculateTotalZipSize(jobDir)
	appLog.GeneralLog.Info("Total ZIP size calculated", "job", jobID, "size", formatSize(zipSize))

	if mailTo != "" {
		queueID, err := sendCompletionEmail(mailTo, jobID, completed, failed, total, zipSize, smtp, archiveBaseURL)
		if err != nil {
			appLog.ErrorLog.Error("Failed to send completion email", "error", err, "to", mailTo)
		} else {
			appLog.GeneralLog.Info("Completion email sent successfully", "to", mailTo, "queue_id", queueID)
			fmt.Printf("✅ EMAIL SENT - To: %s | Queue ID: %s | Size: %s\n", mailTo, queueID, formatSize(zipSize))
		}
	}

	return nil
}

func sendCompletionEmail(to, jobID string, completed, failed, total, zipSize int64, cfg SMTPConfig, archiveBaseURL string) (string, error) {
	subject := fmt.Sprintf("Archive Export Complete - Job %s", jobID)
	boundary := "----=_Part_0_1234567890.1234567890"

	var message strings.Builder

	// Critical headers
	message.WriteString(fmt.Sprintf("Date: %s\r\n", time.Now().Format(time.RFC1123Z)))
	message.WriteString(fmt.Sprintf("Message-ID: <%s@%s>\r\n", generateMessageID(), cfg.Host))
	message.WriteString(fmt.Sprintf("From: %s <%s>\r\n", cfg.FromName, cfg.From))
	message.WriteString(fmt.Sprintf("To: %s\r\n", to))
	message.WriteString(fmt.Sprintf("Subject: %s\r\n", subject))
	message.WriteString("MIME-Version: 1.0\r\n")
	message.WriteString(fmt.Sprintf("Content-Type: multipart/alternative; boundary=\"%s\"\r\n", boundary))
	message.WriteString("\r\n")

	// Plain text version
	message.WriteString(fmt.Sprintf("--%s\r\n", boundary))
	message.WriteString("Content-Type: text/plain; charset=UTF-8\r\n")
	message.WriteString("\r\n")
	message.WriteString(buildPlainTextEmail(jobID, completed, failed, total, zipSize, archiveBaseURL))
	message.WriteString("\r\n")

	// HTML version
	message.WriteString(fmt.Sprintf("--%s\r\n", boundary))
	message.WriteString("Content-Type: text/html; charset=UTF-8\r\n")
	message.WriteString("\r\n")
	message.WriteString(buildHTMLEmail(jobID, completed, failed, total, zipSize, archiveBaseURL))
	message.WriteString("\r\n")

	message.WriteString(fmt.Sprintf("--%s--\r\n", boundary))

	// Send email with retries
	var lastErr error
	var queueID string
	maxRetries := 3

	for i := 0; i < maxRetries; i++ {
		queueID, lastErr = sendWithQueueID(cfg, to, message.String())
		if lastErr == nil {
			return queueID, nil
		}

		fmt.Printf("Email send attempt %d to %s failed: %v\n", i+1, to, lastErr)

		if i < maxRetries-1 {
			time.Sleep(2 * time.Second)
		}
	}

	return "", fmt.Errorf("failed after %d attempts to %s: %w", maxRetries, to, lastErr)
}

func sendWithQueueID(cfg SMTPConfig, to, message string) (string, error) {
	// Connect with timeout
	conn, err := net.DialTimeout("tcp", cfg.Host+":"+cfg.Port, 10*time.Second)
	if err != nil {
		return "", fmt.Errorf("dial failed: %w", err)
	}

	// Set overall deadline for the entire operation
	conn.SetDeadline(time.Now().Add(30 * time.Second))

	client, err := smtp.NewClient(conn, cfg.Host)
	if err != nil {
		conn.Close()
		return "", fmt.Errorf("create client failed: %w", err)
	}
	defer client.Close()

	// Start TLS with proper config
	tlsConfig := &tls.Config{
		ServerName: cfg.Host,
	}
	if err = client.StartTLS(tlsConfig); err != nil {
		return "", fmt.Errorf("StartTLS failed: %w", err)
	}

	// Authenticate
	auth := smtp.PlainAuth("", cfg.Username, cfg.Password, cfg.Host)
	if err = client.Auth(auth); err != nil {
		return "", fmt.Errorf("auth failed: %w", err)
	}

	// Set sender
	if err = client.Mail(cfg.From); err != nil {
		return "", fmt.Errorf("MAIL FROM failed: %w", err)
	}

	// Set recipient
	if err = client.Rcpt(to); err != nil {
		return "", fmt.Errorf("RCPT TO failed: %w", err)
	}

	// Send message data
	writer, err := client.Data()
	if err != nil {
		return "", fmt.Errorf("DATA command failed: %w", err)
	}

	_, err = writer.Write([]byte(message))
	if err != nil {
		writer.Close()
		return "", fmt.Errorf("write message failed: %w", err)
	}

	// Close writer - this sends the final "." and gets the server response
	err = writer.Close()
	queueID := "sent"

	if err != nil {
		return "", fmt.Errorf("close writer failed: %w", err)
	}

	queueID = fmt.Sprintf("sent-%s", generateMessageID())

	// Quit gracefully
	client.Quit()

	return queueID, nil
}

func generateMessageID() string {
	return fmt.Sprintf("%d.%d", time.Now().UnixNano(), os.Getpid())
}

func buildPlainTextEmail(jobID string, completed, failed, total, zipSize int64, archiveBaseURL string) string {
	var msg strings.Builder

	msg.WriteString("Your archive export is complete.\r\n\r\n")
	msg.WriteString(fmt.Sprintf("Job ID: %s\r\n", jobID))
	msg.WriteString(fmt.Sprintf("Total Files: %d | Success: %d | Failed: %d\r\n", total, completed, failed))
	msg.WriteString(fmt.Sprintf("Total ZIP Size: %s\r\n\r\n", formatSize(zipSize)))
	msg.WriteString("Click the link below and log in to view all archive files.\r\n")
	msg.WriteString("Once logged in, click Download to save the archive.\r\n\r\n")
	msg.WriteString(fmt.Sprintf("%s/%s\r\n\r\n", strings.TrimRight(archiveBaseURL, "/"), jobID))
	msg.WriteString("---\r\n")
	msg.WriteString("Archive Export Service\r\n")

	return msg.String()
}

func buildHTMLEmail(jobID string, completed, failed, total, zipSize int64, archiveBaseURL string) string {
	downloadURL := fmt.Sprintf("%s/%s", strings.TrimRight(archiveBaseURL, "/"), jobID)

	return fmt.Sprintf(`<!DOCTYPE html>
<html>
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
</head>
<body style="margin: 0; padding: 0; font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, 'Helvetica Neue', Arial, sans-serif; background-color: #f5f5f5;">
    <table width="100%%" cellpadding="0" cellspacing="0" border="0" style="background-color: #f5f5f5; padding: 40px 20px;">
        <tr>
            <td align="center">
                <table width="600" cellpadding="0" cellspacing="0" border="0" style="background-color: #ffffff; border: 1px solid #e0e0e0;">
                    
                    <!-- Header -->
                    <tr>
                        <td style="background-color: #2c3e50; padding: 24px 30px;">
                            <h1 style="margin: 0; color: #ffffff; font-size: 18px; font-weight: 600;">Archive Export Complete</h1>
                        </td>
                    </tr>
                    
                    <!-- Content -->
                    <tr>
                        <td style="padding: 30px;">
                            
                            <p style="margin: 0 0 20px 0; color: #333333; font-size: 14px; line-height: 1.6;">
                                Your archive export has completed successfully.
                            </p>
                            
                            <!-- Job Details -->
                            <table width="100%%" cellpadding="0" cellspacing="0" border="0" style="margin-bottom: 24px; border: 1px solid #e0e0e0;">
                                <tr style="background-color: #fafafa;">
                                    <td style="padding: 12px 16px; border-bottom: 1px solid #e0e0e0;">
                                        <span style="color: #666666; font-size: 12px;">Job ID</span>
                                    </td>
                                    <td style="padding: 12px 16px; border-bottom: 1px solid #e0e0e0;">
                                        <span style="color: #333333; font-size: 13px; font-family: monospace;">%s</span>
                                    </td>
                                </tr>
                                <tr>
                                    <td style="padding: 12px 16px; border-bottom: 1px solid #e0e0e0;">
                                        <span style="color: #666666; font-size: 12px;">Total Files</span>
                                    </td>
                                    <td style="padding: 12px 16px; border-bottom: 1px solid #e0e0e0;">
                                        <span style="color: #333333; font-size: 13px;">%d</span>
                                    </td>
                                </tr>
                                <tr style="background-color: #fafafa;">
                                    <td style="padding: 12px 16px; border-bottom: 1px solid #e0e0e0;">
                                        <span style="color: #666666; font-size: 12px;">Successful</span>
                                    </td>
                                    <td style="padding: 12px 16px; border-bottom: 1px solid #e0e0e0;">
                                        <span style="color: #27ae60; font-size: 13px; font-weight: 600;">%d</span>
                                    </td>
                                </tr>
                                <tr>
                                    <td style="padding: 12px 16px; border-top: 1px solid #e0e0e0;">
                                        <span style="color: #666666; font-size: 12px;">Failed</span>
                                    </td>
                                    <td style="padding: 12px 16px; border-top: 1px solid #e0e0e0;">
                                        <span style="color: %s; font-size: 13px; font-weight: 600;">%d</span>
                                    </td>
                                </tr>
                                <tr style="background-color: #fafafa;">
                                    <td style="padding: 12px 16px; border-top: 1px solid #e0e0e0;">
                                        <span style="color: #666666; font-size: 12px;">Total Size</span>
                                    </td>
                                    <td style="padding: 12px 16px; border-top: 1px solid #e0e0e0;">
                                        <span style="color: #333333; font-size: 13px; font-weight: 600;">%s</span>
                                    </td>
                                </tr>
                            </table>
                            
                            <p style="margin: 0 0 20px 0; color: #555555; font-size: 13px; line-height: 1.6;">
                                Click the link below and log in to view all archive files. Once logged in, click <strong>Download</strong> to save the archive.
                            </p>
                            
                            <!-- Download Link -->
                            <table width="100%%" cellpadding="0" cellspacing="0" border="0">
                                <tr>
                                    <td align="center" style="padding: 20px 0;">
                                        <a href="%s" target="_blank" 
                                           style="display: inline-block; background-color: #2c3e50; color: #ffffff; text-decoration: none; 
                                                  font-size: 14px; font-weight: 500; padding: 12px 32px; border-radius: 4px;">
                                            Access Archive
                                        </a>
                                    </td>
                                </tr>
                            </table>
                            
                        </td>
                    </tr>
                    
                    <!-- Footer -->
                    <tr>
                        <td style="background-color: #fafafa; padding: 20px 30px; border-top: 1px solid #e0e0e0;">
                            <p style="margin: 0; color: #999999; font-size: 11px; text-align: center;">
                                Archive Export Service | This is an automated message, please do not reply
                            </p>
                        </td>
                    </tr>
                    
                </table>
            </td>
        </tr>
    </table>
</body>
</html>`, jobID, total, completed, getFailedColor(failed), failed, formatSize(zipSize), downloadURL)
}

func getFailedColor(failed int64) string {
	if failed > 0 {
		return "#e74c3c"
	}
	return "#999999"
}

func calculateTotalZipSize(jobDir string) int64 {
	var totalSize int64
	_ = filepath.Walk(jobDir, func(path string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() && strings.HasSuffix(strings.ToLower(info.Name()), ".zip") {
			totalSize += info.Size()
		}
		return nil
	})
	return totalSize
}

func formatSize(b int64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := int64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.2f %cB", float64(b)/float64(div), "KMGTPE"[exp])
}

func copyFile(src, dst string) error {
	source, err := os.Open(src)
	if err != nil {
		return err
	}
	defer source.Close()

	destination, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer destination.Close()

	_, err = io.Copy(destination, source)
	return err
}
