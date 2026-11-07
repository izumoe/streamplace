import { Link, useMatchRoute } from "@tanstack/react-router";
import {
  House,
  LayoutDashboard,
  LogIn,
  Settings,
  Users,
  Video,
} from "lucide-react";
import { useTranslation } from "react-i18next";
import { brandingLinks } from "../lib/branding";
import { useSession } from "../lib/session";
import { useStore } from "../lib/store";
import {
  Sidebar,
  SidebarContent,
  SidebarFooter,
  SidebarGroup,
  SidebarGroupLabel,
  SidebarHeader,
  SidebarMenu,
  SidebarMenuButton,
  SidebarMenuItem,
  SidebarSeparator,
} from "./ui/sidebar";

export default function SidebarComponent() {
  const { t } = useTranslation("common");
  const { state } = useSession();
  const isAuthenticated = state.status === "authenticated";
  const matchRoute = useMatchRoute();
  const socialData = useStore((state) => state.branding?.socialLinks?.data);
  const legalData = useStore((state) => state.branding?.legalLinks?.data);
  const socialLinks = brandingLinks(socialData);
  const legalLinks = brandingLinks(legalData);

  // Active state follows the current route. Home matches exactly (the
  // root route is an ancestor of every page); the others match their
  // sub-routes too (e.g. /settings/account highlights Settings).
  const isHomeActive = matchRoute({ to: "/", fuzzy: false }) !== false;
  const isVideosActive = matchRoute({ to: "/videos", fuzzy: false }) !== false;
  const isDashboardActive =
    matchRoute({ to: "/dashboard", fuzzy: true }) !== false;
  const isSettingsActive =
    matchRoute({ to: "/settings", fuzzy: true }) !== false;

  return (
    <Sidebar side="left" collapsible="icon" variant="sidebar">
      <SidebarHeader className="h-10" />
      <SidebarContent>
        <SidebarGroup>
          <SidebarMenu>
            <SidebarMenuItem>
              <Link to="/" className="w-full">
                <SidebarMenuButton
                  tooltip={t("nav-home")}
                  isActive={isHomeActive}
                >
                  <House />
                  <span>{t("nav-home")}</span>
                </SidebarMenuButton>
              </Link>
            </SidebarMenuItem>
            <SidebarMenuItem>
              <Link to="/videos" className="w-full">
                <SidebarMenuButton
                  tooltip={t("nav-videos")}
                  isActive={isVideosActive}
                >
                  <Video />
                  <span>{t("nav-videos")}</span>
                </SidebarMenuButton>
              </Link>
            </SidebarMenuItem>
            <SidebarMenuItem>
              <SidebarMenuButton tooltip={t("nav-following")} disabled>
                <Users />
                <span>{t("nav-following")}</span>
              </SidebarMenuButton>
            </SidebarMenuItem>
            {isAuthenticated && (
              <SidebarMenuItem>
                <Link to="/dashboard" className="w-full">
                  <SidebarMenuButton
                    tooltip={t("nav-dashboard", { defaultValue: "Dashboard" })}
                    isActive={isDashboardActive}
                  >
                    <LayoutDashboard />
                    <span>
                      {t("nav-dashboard", { defaultValue: "Dashboard" })}
                    </span>
                  </SidebarMenuButton>
                </Link>
              </SidebarMenuItem>
            )}
          </SidebarMenu>
        </SidebarGroup>

        <SidebarSeparator />

        <SidebarGroup>
          <SidebarGroupLabel>{t("nav-account")}</SidebarGroupLabel>
          <SidebarMenu>
            <SidebarMenuItem>
              <SidebarMenuButton
                tooltip={t("log-in")}
                onClick={() => useStore.getState().openLoginModal()}
              >
                <LogIn />
                <span>{t("log-in")}</span>
              </SidebarMenuButton>
            </SidebarMenuItem>
            <SidebarMenuItem>
              <Link to="/settings" className="w-full">
                <SidebarMenuButton
                  tooltip={t("nav-settings")}
                  isActive={isSettingsActive}
                >
                  <Settings />
                  <span>{t("nav-settings")}</span>
                </SidebarMenuButton>
              </Link>
            </SidebarMenuItem>
          </SidebarMenu>
        </SidebarGroup>
      </SidebarContent>

      {(socialLinks.length > 0 || legalLinks.length > 0) && (
        <SidebarFooter className="group-data-[collapsible=icon]:hidden">
          {socialLinks.length > 0 && (
            <nav
              aria-label={t("site-social-links", "Social links")}
              className="flex flex-wrap gap-x-4 gap-y-2 px-2 text-sm"
            >
              {socialLinks.map((link, index) => (
                <a
                  key={index}
                  href={link.url}
                  target="_blank"
                  rel="noopener noreferrer"
                  className="focus-visible:outline-ring hover:underline focus-visible:outline"
                >
                  {link.label}
                </a>
              ))}
            </nav>
          )}
          {legalLinks.length > 0 && (
            <nav
              aria-label={t("site-legal-links", "Legal links")}
              className="text-muted-foreground flex flex-wrap gap-x-4 gap-y-2 px-2 text-xs"
            >
              {legalLinks.map((link, index) => (
                <a
                  key={index}
                  href={link.url}
                  target="_blank"
                  rel="noopener noreferrer"
                  className="focus-visible:outline-ring hover:underline focus-visible:outline"
                >
                  {link.label}
                </a>
              ))}
            </nav>
          )}
        </SidebarFooter>
      )}
    </Sidebar>
  );
}
