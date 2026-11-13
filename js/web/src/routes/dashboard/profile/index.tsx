import { BioSettings } from "@/components/bio/bio-settings";
import { createFileRoute } from "@tanstack/react-router";

export const Route = createFileRoute("/dashboard/profile/")({
  component: ProfileSettingsPage,
});

function ProfileSettingsPage() {
  return (
    <section id="bio" className="w-full py-6">
      <BioSettings />
    </section>
  );
}
